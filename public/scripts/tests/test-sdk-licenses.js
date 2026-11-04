const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const zlib = require("node:zlib");
const licenses = require("../sdk-licenses");

function temporaryRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "sdk-licenses-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function pkg(root, name, license = "MIT") {
  const directory = path.join(root, name);
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, "LICENSE"), `License for ${name}`);
  return {
    id: name,
    name,
    version: "1.0.0",
    license,
    authors: ["Example author"],
    source: "registry+https://github.com/rust-lang/crates.io-index",
    manifest_path: path.join(directory, "Cargo.toml"),
  };
}

function upstreamFixture(t) {
  const root = temporaryRoot(t);
  const dependency = pkg(root, "attributed");
  fs.unlinkSync(path.join(root, "attributed/LICENSE"));
  const revision = "a".repeat(40);
  fs.writeFileSync(
    path.join(root, "attributed/.cargo_vcs_info.json"),
    JSON.stringify({ git: { sha1: revision } }),
  );
  const directory = path.join(root, "upstream");
  fs.mkdirSync(directory);
  const text =
    "Copyright (c) 2019 Actual Upstream Owner\n\nPermission is hereby granted...\n";
  const file = path.join(directory, "LICENSE-MIT");
  fs.writeFileSync(file, text);
  const checksum = "b".repeat(64);
  return {
    root,
    dependency,
    text,
    file,
    upstream: {
      directory,
      checksums: new Map([["attributed@1.0.0", checksum]]),
      overrides: {
        "attributed@1.0.0": {
          declared_license: "MIT",
          license: "MIT",
          revision,
          crate_sha256: checksum,
          files: [
            {
              file: "LICENSE-MIT",
              source_url: `https://raw.githubusercontent.com/example/project/${revision}/LICENSE-MIT`,
              sha256: crypto.createHash("sha256").update(text).digest("hex"),
            },
          ],
        },
      },
    },
  };
}

function tarFile(name, contents) {
  const header = Buffer.alloc(512);
  header.write(name);
  header.write(contents.length.toString(8).padStart(11, "0"), 124);
  header.write("0", 156);
  return zlib.gzipSync(
    Buffer.concat([
      header,
      Buffer.from(contents),
      Buffer.alloc((512 - (contents.length % 512)) % 512),
      Buffer.alloc(1024),
    ]),
  );
}

test("dependency inventory excludes development-only packages", () => {
  const metadata = {
    packages: [
      { id: "root", name: "sdk", source: null },
      { id: "dev", name: "dev", source: "registry+example" },
    ],
    resolve: {
      nodes: [
        { id: "root", deps: [{ pkg: "dev", dep_kinds: [{ kind: "dev" }] }] },
        { id: "dev", deps: [] },
      ],
    },
  };
  assert.deepEqual(licenses.dependencies(metadata, "sdk"), []);
});

test("dependency inventory includes transitive build dependencies", () => {
  const metadata = {
    packages: [
      { id: "root", name: "sdk", source: null },
      { id: "build", name: "build", source: "registry+example" },
      { id: "runtime", name: "runtime", source: "registry+example" },
    ],
    resolve: {
      nodes: [
        {
          id: "root",
          deps: [{ pkg: "build", dep_kinds: [{ kind: "build" }] }],
        },
        {
          id: "build",
          deps: [{ pkg: "runtime", dep_kinds: [{ kind: null }] }],
        },
        { id: "runtime", deps: [] },
      ],
    },
  };
  assert.deepEqual(
    licenses.dependencies(metadata, "sdk").map((p) => p.name),
    ["build", "runtime"],
  );
});

test("notices include licenses for vendored native code", (t) => {
  const root = temporaryRoot(t);
  const p = pkg(root, "wrapper", "CC0-1.0");
  const native = path.join(root, "wrapper/depend/secp256k1");
  fs.mkdirSync(native, { recursive: true });
  fs.writeFileSync(path.join(native, "COPYING"), "Native MIT notice");
  assert.ok(
    licenses
      .packageNotices(p, root)
      .files.some(
        ([file, text]) =>
          file === "depend/secp256k1/COPYING" && text === "Native MIT notice",
      ),
  );
});

test("missing license text fails instead of producing incomplete notices", (t) => {
  const root = temporaryRoot(t);
  const p = pkg(root, "unknown", "Unrecognized-license");
  fs.unlinkSync(path.join(root, "unknown/LICENSE"));
  assert.throws(() => licenses.packageNotices(p, root), /No license text/);
});

test("MIT fallback rejects a generic attribution template", (t) => {
  const root = temporaryRoot(t);
  const p = pkg(root, "dual", "MIT OR Apache-2.0");
  fs.unlinkSync(path.join(root, "dual/LICENSE"));
  fs.writeFileSync(
    path.join(root, "MIT.txt"),
    "Copyright (c) <year> <copyright holders>\nPermission is hereby granted...",
  );
  assert.throws(
    () => licenses.packageNotices(p, root),
    /add a reviewed upstream override/,
  );
});

test("upstream overrides preserve the actual copyright notice", (t) => {
  const { root, dependency, text, upstream } = upstreamFixture(t);
  assert.equal(
    licenses.packageNotices(dependency, root, upstream).files[0][1],
    text,
  );
});

test("upstream overrides reject a modified notice file", (t) => {
  const { root, dependency, file, upstream } = upstreamFixture(t);
  fs.writeFileSync(file, "Changed attribution");
  assert.throws(
    () => licenses.packageNotices(dependency, root, upstream),
    /Upstream notice checksum mismatch/,
  );
});

test("upstream overrides must match the locked crate checksum", (t) => {
  const { root, dependency, upstream } = upstreamFixture(t);
  upstream.checksums.set("attributed@1.0.0", "c".repeat(64));
  assert.throws(
    () => licenses.packageNotices(dependency, root, upstream),
    /Upstream override crate checksum mismatch/,
  );
});

test("upstream overrides must match the published source revision", (t) => {
  const { root, dependency, upstream } = upstreamFixture(t);
  fs.writeFileSync(
    path.join(root, "attributed/.cargo_vcs_info.json"),
    JSON.stringify({ git: { sha1: "c".repeat(40) } }),
  );
  assert.throws(
    () => licenses.packageNotices(dependency, root, upstream),
    /Upstream override revision mismatch/,
  );
});

test("registry source links identify the exact crate release", () => {
  assert.equal(
    licenses.sourceUrl({
      name: "uniffi_core",
      version: "0.28.3",
      source: "registry+https://github.com/rust-lang/crates.io-index",
    }),
    "https://crates.io/api/v1/crates/uniffi_core/0.28.3/download",
  );
});

test("MPL source verification rejects a modified source file", (t) => {
  const root = temporaryRoot(t);
  const source = path.join(root, "src/index/uniffi_core-0.28.3");
  const cache = path.join(root, "cache/index");
  fs.mkdirSync(source, { recursive: true });
  fs.mkdirSync(cache, { recursive: true });
  const archive = tarFile("uniffi_core-0.28.3/lib.rs", "Original source");
  fs.writeFileSync(path.join(cache, "uniffi_core-0.28.3.crate"), archive);
  fs.writeFileSync(path.join(source, "lib.rs"), "Modified source");
  const checksum = crypto.createHash("sha256").update(archive).digest("hex");
  assert.throws(
    () =>
      licenses.verifyMplSource(
        {
          name: "uniffi_core",
          version: "0.28.3",
          license: "MPL-2.0",
          source: "registry+https://github.com/rust-lang/crates.io-index",
          manifest_path: path.join(source, "Cargo.toml"),
        },
        new Map([["uniffi_core@0.28.3", checksum]]),
      ),
    /Modified MPL source/,
  );
});

function reportFixture(t, rustNotices = "<body>Runtime notices</body>") {
  const root = temporaryRoot(t);
  const dependency = pkg(root, "dependency");
  const output = licenses.report(
    {
      packages: [{ id: "root", name: "sdk", source: null }, dependency],
      resolve: {
        nodes: [
          {
            id: "root",
            deps: [{ pkg: dependency.id, dep_kinds: [{ kind: null }] }],
          },
          { id: dependency.id, deps: [], features: [] },
        ],
      },
    },
    "sdk",
    {
      canonicalDirectory: root,
      checksums: new Map(),
      group: "frost-wasm",
      artifacts: { "binding.wasm": "digest" },
      lockHash: "lock-digest",
      rustc: "rustc 1.92.0",
      rustCommit: "a".repeat(40),
      rustNotices,
    },
  );
  return { root, output };
}

test("reports do not disclose local source paths", (t) => {
  const { root, output } = reportFixture(t);
  assert.ok(!JSON.stringify(output).includes(root));
});

test("Rust notices are copied without changing the upstream report", (t) => {
  const rustNotices =
    '<!doctype html>\r\n<body><a href="https://example.com/source">Source</a>  \r\n</body>\r\n';
  const { output } = reportFixture(t, rustNotices);
  assert.equal(output["RUST-NOTICES.html"], rustNotices);
});

test("text notices retain source download URLs", (t) => {
  const { output } = reportFixture(t);
  assert.match(
    output["NOTICES.txt"],
    /Source: https:\/\/crates\.io\/api\/v1\/crates\/dependency\/1\.0\.0\/download/,
  );
});

test("Bare report settings reject an unconfigured platform", () => {
  assert.throws(
    () =>
      licenses.settings(
        { groups: { "frost-bare": { platform: "bare", outputs: [] } } },
        "frost-bare",
        "unexpected",
      ),
    /configured Bare platform/,
  );
});

test("inventory verification rejects a changed binary", (t) => {
  const root = temporaryRoot(t);
  const inventoryPath =
    "sdks/js/packages/spark-sdk/third-party/frost-wasm/inventory.json";
  fs.mkdirSync(path.dirname(path.join(root, inventoryPath)), {
    recursive: true,
  });
  fs.writeFileSync(path.join(root, "binding.wasm"), "Changed binary");
  fs.writeFileSync(
    path.join(root, inventoryPath),
    JSON.stringify({
      schema_version: 1,
      rust_notices_file: "RUST-NOTICES.html",
      group: "frost-wasm",
      artifacts: { "binding.wasm": "old-digest" },
    }),
  );
  assert.throws(
    () =>
      licenses.verifyReports(
        {
          groups: {
            "frost-wasm": {
              platform: "wasm",
              family: "frost",
              outputs: ["binding.wasm", inventoryPath],
            },
          },
        },
        [],
        { root },
      ),
    /License inventory is stale/,
  );
});
