const assert = require("assert");
const childProcess = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");
const test = require("node:test");

const sdkBindings = require("../sdk-bindings");

const config = sdkBindings.loadConfig();

function temporaryRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "sdk-bindings-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function selected(relativePath) {
  return new Set(sdkBindings.selectGroups(config, [relativePath]));
}

function assertSetEqual(actual, expected) {
  assert.deepStrictEqual([...actual].sort(), [...expected].sort());
}

test("shared Cargo input selects every group", () => {
  assertSetEqual(
    selected("signer/Cargo.lock"),
    new Set(Object.keys(config.groups)),
  );
});

test("shared validation proto selects every group", () => {
  assertSetEqual(
    selected("protos/validate/validate.proto"),
    new Set(Object.keys(config.groups)),
  );
});

test("FROST source selects every FROST platform and Bare", () => {
  assertSetEqual(
    selected("signer/spark-frost/src/signing.rs"),
    new Set(["frost-wasm", "frost-android", "frost-ios", "frost-bare"]),
  );
});

test("token source selects only token platforms", () => {
  assertSetEqual(
    selected("signer/spark-token-primitives/src/lib.rs"),
    new Set(["token-wasm", "token-android", "token-ios"]),
  );
});

test("WASM patch selects its owning group", () => {
  assertSetEqual(
    selected(
      "sdks/js/packages/spark-sdk/wasm/patch-token-primitives-wasm-browser.mjs",
    ),
    new Set(["token-wasm"]),
  );
});

test("Android verifier selects both Android groups", () => {
  assertSetEqual(
    selected("public/scripts/verify-android-min-api.sh"),
    new Set(["frost-android", "token-android"]),
  );
});

test("iOS template selects its owning iOS group", () => {
  assertSetEqual(
    selected(
      "signer/spark-frost-uniffi/spark-frost-swift/spark_frostFFI.xcframework/Info.plist",
    ),
    new Set(["frost-ios"]),
  );
});

test("UniFFI Cargo configs select every platform for their family", () => {
  assertSetEqual(
    selected("signer/spark-frost-uniffi/.cargo/config.toml"),
    new Set(["frost-wasm", "frost-android", "frost-ios"]),
  );
  assertSetEqual(
    selected("signer/spark-token-primitives-uniffi/.cargo/config.toml"),
    new Set(["token-wasm", "token-android", "token-ios"]),
  );
});

test("public lockfile selects only Bare", () => {
  assertSetEqual(selected("sdks/js/yarn.lock"), new Set(["frost-bare"]));
});

test("public lockfile triggers Bare without crossing the source digest boundary", () => {
  assert.ok(
    config.groups["frost-bare"].trigger_inputs.includes("sdks/js/yarn.lock"),
  );
  assert.ok(!config.groups["frost-bare"].inputs.includes("sdks/js/yarn.lock"));
});

test("Bare build tool versions must match the synced provenance", () => {
  const localConfig = {
    bare_tool_versions: { "bare-make": "1.6.3", "cmake-runtime": "4.1.1" },
  };
  sdkBindings.verifyBareToolVersions(localConfig, {
    installedVersions: { "bare-make": "1.6.3", "cmake-runtime": "4.1.1" },
  });
  assert.throws(
    () =>
      sdkBindings.verifyBareToolVersions(localConfig, {
        installedVersions: {
          "bare-make": "1.7.0",
          "cmake-runtime": "4.1.1",
        },
      }),
    /bare-make: expected 1\.6\.3, got 1\.7\.0/,
  );
});

test("configured workflow changes select all groups", () => {
  assertSetEqual(
    selected(".github/workflows/sdk-bindings.yaml"),
    new Set(Object.keys(config.groups)),
  );
});

test("generated outputs select their owning group", () => {
  for (const [groupName, group] of Object.entries(config.groups)) {
    for (const output of group.outputs) {
      assertSetEqual(selected(output), new Set([groupName]));
    }
  }
});

test("unrelated files select no groups", () => {
  assertSetEqual(selected("spark/so/handler.go"), new Set());
});

test("input expansion includes tracked and untracked repository files", (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "tracked"), { recursive: true });
  fs.writeFileSync(path.join(root, "tracked/input.rs"), "tracked");
  fs.writeFileSync(path.join(root, "new.txt"), "untracked");

  const expanded = sdkBindings.expandInputPatterns(
    ["tracked/**", "new.txt"],
    "test input",
    {
      root,
      repositoryPaths: ["tracked/input.rs", "new.txt"],
    },
  );

  assert.deepStrictEqual(expanded, [
    path.join(root, "new.txt"),
    path.join(root, "tracked/input.rs"),
  ]);
});

test("input expansion rejects a pattern with no repository file", (t) => {
  const root = temporaryRoot(t);
  assert.throws(
    () =>
      sdkBindings.expandInputPatterns(["missing/**"], "test input", {
        root,
        repositoryPaths: [],
      }),
    /pattern matched no files/,
  );
});

test("exact output patterns do not recursively accept directories", (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "generated"));
  fs.writeFileSync(path.join(root, "generated/output.bin"), "generated");

  assert.throws(
    () => sdkBindings.expandPatterns(["generated"], "test output", { root }),
    /pattern matched no files/,
  );
});

test("changed paths use the requested git range", () => {
  let invocation;
  const paths = sdkBindings.changedPaths("base", "head", {
    root: "/repo",
    execFileSync(command, args, options) {
      invocation = { command, args, options };
      return "one.rs\ntwo.rs\n";
    },
  });

  assert.deepStrictEqual(paths, ["one.rs", "two.rs"]);
  assert.deepStrictEqual(invocation, {
    command: "git",
    args: [
      "diff",
      "--no-renames",
      "--name-only",
      "--diff-filter=ACMRD",
      "base",
      "head",
    ],
    options: { cwd: "/repo", encoding: "utf8" },
  });
});

test("missing base forces all groups", () => {
  let force;
  const localConfig = { groups: { one: {}, two: {} } };
  const result = sdkBindings.detectCommand(
    localConfig,
    { base: undefined, head: "head" },
    {
      changedPaths: () => [],
      selectGroups: (_config, _paths, selectedForce) => {
        force = selectedForce;
        return ["one", "two"];
      },
      log: () => {},
      env: {},
    },
  );

  assert.strictEqual(force, "all");
  assert.deepStrictEqual(result, ["one", "two"]);
});

test("detection writes its complete GitHub output contract", (t) => {
  const root = temporaryRoot(t);
  const output = path.join(root, "github-output");
  const localConfig = {
    groups: {
      "frost-wasm": { platform: "wasm" },
      "token-android": { platform: "android" },
      "frost-ios": { platform: "ios" },
      "frost-bare": { platform: "bare" },
    },
  };

  sdkBindings.writeDetectionOutputs(
    localConfig,
    ["frost-wasm", "frost-bare"],
    output,
  );

  assert.strictEqual(
    fs.readFileSync(output, "utf8"),
    "frost_wasm=true\n" +
      "token_android=false\n" +
      "frost_ios=false\n" +
      "frost_bare=true\n" +
      "wasm=true\n" +
      "android=false\n" +
      "ios=false\n" +
      "bare=true\n" +
      "any=true\n" +
      "groups=frost-wasm,frost-bare\n",
  );
});

test("source hashing ignores only configured release version metadata", (t) => {
  const root = temporaryRoot(t);
  const packagePath = path.join(root, "package.json");
  const localConfig = {
    source_hash_transforms: {
      "package.json": { remove_top_level_json_fields: ["version"] },
    },
  };
  fs.writeFileSync(
    packagePath,
    JSON.stringify({
      name: "@example/addon",
      scripts: { build: "make" },
      version: "1.0.0",
    }),
  );
  const first = sdkBindings.sourceFileHash(localConfig, packagePath, { root });
  fs.writeFileSync(
    packagePath,
    JSON.stringify({
      name: "@example/addon",
      scripts: { build: "make" },
      version: "1.0.1",
    }),
  );
  const versionBump = sdkBindings.sourceFileHash(localConfig, packagePath, {
    root,
  });
  fs.writeFileSync(
    packagePath,
    JSON.stringify({
      name: "@example/addon",
      scripts: { build: "make all" },
      version: "1.0.1",
    }),
  );
  const buildChange = sdkBindings.sourceFileHash(localConfig, packagePath, {
    root,
  });

  assert.strictEqual(first, versionBump);
  assert.notStrictEqual(first, buildChange);
});

test("source hashing canonicalizes only safe integer numbers", (t) => {
  const root = temporaryRoot(t);
  const packagePath = path.join(root, "package.json");
  const localConfig = {
    source_hash_transforms: {
      "package.json": { remove_top_level_json_fields: ["version"] },
    },
  };
  fs.writeFileSync(
    packagePath,
    '{"name":"@example/addon","retries":1,"scripts":{"build":"make"},"version":"1.0.0"}',
  );
  const integer = sdkBindings.sourceFileHash(localConfig, packagePath, {
    root,
  });
  fs.writeFileSync(
    packagePath,
    '{"version":"1.0.1","scripts":{"build":"make"},"retries":1.0,"name":"@example/addon"}',
  );
  const decimalInteger = sdkBindings.sourceFileHash(localConfig, packagePath, {
    root,
  });

  fs.writeFileSync(
    packagePath,
    '{"name":"@example/addon","ratio":1.5,"version":"1.0.0"}',
  );
  assert.throws(
    () => sdkBindings.sourceFileHash(localConfig, packagePath, { root }),
    /safe integers/,
  );
  fs.writeFileSync(
    packagePath,
    '{"name":"@example/addon","count":9007199254740992,"version":"1.0.0"}',
  );
  assert.throws(
    () => sdkBindings.sourceFileHash(localConfig, packagePath, { root }),
    /safe integers/,
  );

  assert.strictEqual(
    integer,
    "94cfc6e715a63814a9c413e499781d86838609d60ad4065b3d91f7712b96a5a2",
  );
  assert.strictEqual(integer, decimalInteger);
});

test("source digests retain the manifest hashing format", () => {
  const hashes = new Map([
    ["a.txt", "00".repeat(32)],
    ["b.txt", "ff".repeat(32)],
  ]);
  assert.strictEqual(
    sdkBindings.sourceDigest(
      { tool_versions: { z: "2", a: "1" } },
      "frost",
      ["a.txt", "b.txt"],
      hashes,
    ),
    "003a48a7a94e0d161148eee59b69a7ff0f38c710d02120b7f36eb3bb4fc3e059",
  );
});

test("manifest validation rejects undeclared metadata", (t) => {
  const root = temporaryRoot(t);
  const artifact = path.join(root, "artifact.bin");
  fs.writeFileSync(artifact, "generated");
  const localConfig = {
    schema_version: 1,
    manifest: "manifest.json",
    groups: { frost: { outputs: ["artifact.bin"] } },
  };
  const manifest = {
    schema_version: 1,
    groups: {
      frost: {
        input_sha256: "source",
        input_file_count: 1,
        artifacts: { "artifact.bin": sdkBindings.fileHash(artifact) },
        private_metadata: "not part of the public schema",
      },
    },
  };
  fs.writeFileSync(path.join(root, "manifest.json"), JSON.stringify(manifest));

  assert.throws(
    () =>
      sdkBindings.checkManifest(localConfig, [], {
        root,
        sourceHash: () => ["source", 1],
      }),
    /unexpected manifest field/,
  );
});

test("manifest validation reports missing, added, and changed artifacts", (t) => {
  const root = temporaryRoot(t);
  const localConfig = {
    schema_version: 1,
    manifest: "manifest.json",
    groups: { frost: { outputs: ["artifact.bin"] } },
  };
  fs.writeFileSync(
    path.join(root, "manifest.json"),
    JSON.stringify({
      schema_version: 1,
      groups: {
        frost: {
          input_sha256: "source",
          input_file_count: 1,
          artifacts: { "changed.bin": "old", "missing.bin": "old" },
        },
      },
    }),
  );

  assert.throws(
    () =>
      sdkBindings.checkManifest(localConfig, [], {
        root,
        sourceHash: () => ["source", 1],
        artifactHashes: () => ({ "added.bin": "new", "changed.bin": "new" }),
      }),
    (error) =>
      error.message.includes("missing artifact missing.bin") &&
      error.message.includes("unrecorded artifact added.bin") &&
      error.message.includes("artifact changed changed.bin"),
  );
});

test("manifest validation rejects obsolete managed output files", (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "generated"));
  const current = path.join(root, "generated/current.bin");
  fs.writeFileSync(current, "current");
  fs.writeFileSync(path.join(root, "generated/obsolete.bin"), "obsolete");
  const localConfig = {
    schema_version: 1,
    manifest: "manifest.json",
    managed_output_patterns: ["generated/**"],
    groups: { frost: { outputs: ["generated/current.bin"] } },
  };
  fs.writeFileSync(
    path.join(root, "manifest.json"),
    JSON.stringify({
      schema_version: 1,
      groups: {
        frost: {
          input_sha256: "source",
          input_file_count: 1,
          artifacts: { "generated/current.bin": sdkBindings.fileHash(current) },
        },
      },
    }),
  );

  assert.throws(
    () =>
      sdkBindings.checkManifest(localConfig, [], {
        root,
        sourceHash: () => ["source", 1],
      }),
    /unconfigured generated artifact: generated\/obsolete.bin/,
  );
});

test("clean tolerates missing outputs and removes obsolete managed files", (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "generated"));
  const present = path.join(root, "generated/present");
  const obsolete = path.join(root, "generated/obsolete");
  fs.writeFileSync(present, "generated");
  fs.writeFileSync(obsolete, "obsolete");
  const localConfig = {
    managed_output_patterns: ["generated/**"],
    groups: { frost: { outputs: ["generated/present", "generated/missing"] } },
  };

  sdkBindings.cleanOutputs(localConfig, ["frost"], { root });

  assert.strictEqual(fs.existsSync(present), false);
  assert.strictEqual(fs.existsSync(obsolete), false);
});

test("package verification rejects an artifact omitted from npm pack", () => {
  const localConfig = {
    packages: { "@example/sdk": "packages/sdk" },
    groups: { frost: {} },
  };
  const manifest = {
    groups: {
      frost: { artifacts: { "packages/sdk/generated.bin": "digest" } },
    },
  };

  assert.throws(
    () =>
      sdkBindings.verifyPackages(localConfig, {
        checkManifest: () => {},
        manifest,
        packageReports: { "@example/sdk": [{ files: [{ path: "index.js" }] }] },
      }),
    /omits generated/,
  );
});

test("package verification rejects artifacts without a published package owner", () => {
  const localConfig = {
    packages: { "@example/sdk": "packages/sdk" },
    groups: { frost: {} },
  };
  const manifest = {
    groups: { frost: { artifacts: { "elsewhere/file": "digest" } } },
  };

  assert.throws(
    () =>
      sdkBindings.verifyPackages(localConfig, {
        checkManifest: () => {},
        manifest,
      }),
    /No npm package owns/,
  );
});

test("stage copies exact outputs and manifest under repository paths", (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "bindings"));
  fs.writeFileSync(path.join(root, "bindings/generated.bin"), "binding");
  fs.writeFileSync(path.join(root, "manifest.json"), "{}");
  const destination = path.join(root, "staged");
  const localConfig = {
    manifest: "manifest.json",
    groups: { frost: { outputs: ["bindings/generated.bin"] } },
  };

  sdkBindings.stageUpdate(localConfig, destination, { root });

  assert.strictEqual(
    fs.readFileSync(path.join(destination, "bindings/generated.bin"), "utf8"),
    "binding",
  );
  assert.strictEqual(
    fs.readFileSync(path.join(destination, "manifest.json"), "utf8"),
    "{}",
  );
});

test("archive contains selected outputs at repository-relative paths", (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "bindings"));
  fs.writeFileSync(path.join(root, "bindings/generated.bin"), "binding");
  const archive = path.join(root, "archive.tar.gz");
  const localConfig = {
    groups: { frost: { outputs: ["bindings/generated.bin"] } },
  };
  const tarEnvironments = [];

  sdkBindings.createArchive(localConfig, ["frost"], archive, {
    root,
    execFileSync(command, args, options) {
      tarEnvironments.push(options.env);
      return childProcess.execFileSync(command, args, options);
    },
  });

  const entries = childProcess.execFileSync("tar", ["-tzf", archive], {
    encoding: "utf8",
  });
  assert.strictEqual(entries, "bindings/generated.bin\n");
  assert.ok(
    tarEnvironments.every(
      (environment) => environment.COPYFILE_DISABLE === "1",
    ),
  );
});

test("archive rejects unexpected metadata paths", (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "bindings"));
  fs.writeFileSync(path.join(root, "bindings/generated.bin"), "binding");
  const localConfig = {
    groups: { frost: { outputs: ["bindings/generated.bin"] } },
  };

  assert.throws(
    () =>
      sdkBindings.createArchive(
        localConfig,
        ["frost"],
        path.join(root, "archive.tar.gz"),
        {
          root,
          execFileSync(_command, args) {
            return args[0] === "-tzf"
              ? "bindings/._generated.bin\nbindings/generated.bin\n"
              : undefined;
          },
        },
      ),
    /unexpected paths/,
  );
});

test("archive rejects undeclared files in a selected managed output tree", (t) => {
  const root = temporaryRoot(t);
  fs.mkdirSync(path.join(root, "bindings"));
  fs.writeFileSync(path.join(root, "bindings/generated.bin"), "binding");
  fs.writeFileSync(path.join(root, "bindings/new-resource.bin"), "resource");
  const localConfig = {
    managed_output_patterns: ["bindings/**"],
    groups: { frost: { outputs: ["bindings/generated.bin"] } },
  };

  assert.throws(
    () =>
      sdkBindings.createArchive(
        localConfig,
        ["frost"],
        path.join(root, "archive.tar.gz"),
        { root },
      ),
    /undeclared outputs:\n  - bindings\/new-resource\.bin/,
  );
});

test("manifest update and check round trip through repository inputs", (t) => {
  const root = temporaryRoot(t);
  fs.writeFileSync(path.join(root, "input.txt"), "input");
  fs.writeFileSync(path.join(root, "artifact.bin"), "artifact");
  const localConfig = {
    schema_version: 1,
    manifest: "manifest.json",
    common_inputs: ["input.txt"],
    groups: { frost: { inputs: [], outputs: ["artifact.bin"] } },
  };
  const options = { root, repositoryPaths: ["input.txt", "artifact.bin"] };

  sdkBindings.updateManifest(localConfig, [], options);
  sdkBindings.checkManifest(localConfig, [], options);

  const manifest = JSON.parse(
    fs.readFileSync(path.join(root, "manifest.json"), "utf8"),
  );
  assert.strictEqual(manifest.groups.frost.input_file_count, 1);
  assert.strictEqual(
    manifest.groups.frost.artifacts["artifact.bin"],
    sdkBindings.fileHash(path.join(root, "artifact.bin")),
  );
});

test("stage CLI requires and parses an output directory", () => {
  assert.deepStrictEqual(
    sdkBindings.parseArgs(["stage", "--output", "/tmp/bindings"]),
    {
      command: "stage",
      head: "HEAD",
      groups: [],
      output: "/tmp/bindings",
    },
  );
  assert.throws(() => sdkBindings.parseArgs(["stage"]), /requires --output/);
});
