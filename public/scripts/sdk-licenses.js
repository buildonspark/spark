const childProcess = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const zlib = require("node:zlib");

const ROOT = path.resolve(__dirname, "../..");
const LICENSE_NAMES = /^(?:licen[cs]e|copying|copyright|notice)(?:$|[._-])/i;
const CANONICAL_FALLBACKS = new Set(["CC0-1.0", "MPL-2.0"]);

function normalizeNotice(text) {
  return `${text
    .replace(/\r\n?/g, "\n")
    .replace(/[ \t]+$/gm, "")
    .trimEnd()}\n`;
}

function sha256(content) {
  return crypto.createHash("sha256").update(content).digest("hex");
}

function dependencies(metadata, packageName) {
  const packages = new Map(metadata.packages.map((pkg) => [pkg.id, pkg]));
  const nodes = new Map(metadata.resolve.nodes.map((node) => [node.id, node]));
  const root = metadata.packages.find(
    (pkg) => pkg.name === packageName && !pkg.source,
  );
  if (!root) throw new Error(`Missing root package: ${packageName}`);
  const seen = new Set();
  const pending = [root.id];
  while (pending.length) {
    const id = pending.pop();
    if (seen.has(id)) continue;
    seen.add(id);
    const node = nodes.get(id);
    if (!node) throw new Error(`Missing dependency node: ${id}`);
    for (const dep of node.deps) {
      if (dep.dep_kinds.some(({ kind }) => kind !== "dev"))
        pending.push(dep.pkg);
    }
  }
  return [...seen]
    .map((id) => packages.get(id))
    .filter((pkg) => pkg.source)
    .sort((a, b) =>
      `${a.name}@${a.version}:${a.source}`.localeCompare(
        `${b.name}@${b.version}:${b.source}`,
        "en",
      ),
    );
}

function noticeFiles(directory, prefix = "") {
  const files = [];
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    if (
      entry.name.startsWith(".") ||
      ["target", "node_modules"].includes(entry.name)
    )
      continue;
    const relativePath = path.posix.join(prefix, entry.name);
    const absolutePath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...noticeFiles(absolutePath, relativePath));
    } else if (entry.isFile() && LICENSE_NAMES.test(entry.name)) {
      files.push([relativePath, fs.readFileSync(absolutePath, "utf8")]);
    }
  }
  return files.sort(([a], [b]) => a.localeCompare(b, "en"));
}

function sourceUrl(pkg) {
  if (pkg.source.startsWith("registry+")) {
    if (
      !pkg.source.includes("github.com/rust-lang/crates.io-index") &&
      !pkg.source.includes("index.crates.io")
    ) {
      throw new Error(
        `Unrecognized registry; configure a source download URL for ${pkg.name}`,
      );
    }
    return `https://crates.io/api/v1/crates/${pkg.name}/${pkg.version}/download`;
  }
  const match =
    /^git\+(https:\/\/github.com\/[^?#]+)(?:\?[^#]+)?#([a-f0-9]{40})$/.exec(
      pkg.source,
    );
  if (!match)
    throw new Error(`Unrecognized source for ${pkg.name}: ${pkg.source}`);
  return `${match[1].replace(/\.git$/, "")}/tree/${match[2]}`;
}

function archiveFiles(archive) {
  const buffer = zlib.gunzipSync(archive);
  const files = new Map();
  let longName;
  for (let offset = 0; offset + 512 <= buffer.length; ) {
    const header = buffer.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) break;
    const field = (start, size) =>
      header
        .subarray(start, start + size)
        .toString()
        .replace(/\0.*$/s, "");
    const size = parseInt(field(124, 12).trim(), 8) || 0;
    const type = field(156, 1);
    const contents = buffer.subarray(offset + 512, offset + 512 + size);
    const name =
      longName || [field(345, 155), field(0, 100)].filter(Boolean).join("/");
    longName = undefined;
    if (type === "L") longName = contents.toString().replace(/\0.*$/s, "");
    else if (type === "x")
      longName = /\d+ path=([^\n]+)\n/.exec(contents.toString())?.[1];
    else if (type === "0" || type === "") files.set(name, contents);
    else if (type !== "5" && type !== "g")
      throw new Error(`Unsupported crate archive entry type: ${type}`);
    offset += 512 + Math.ceil(size / 512) * 512;
  }
  return files;
}

function lockChecksums(text) {
  return new Map(
    text.split(/^\[\[package\]\]$/m).flatMap((entry) => {
      const name = /^name = "([^"]+)"$/m.exec(entry)?.[1];
      const version = /^version = "([^"]+)"$/m.exec(entry)?.[1];
      const checksum = /^checksum = "([a-f0-9]{64})"$/m.exec(entry)?.[1];
      return checksum ? [[`${name}@${version}`, checksum]] : [];
    }),
  );
}

function verifyMplSource(pkg, checksums) {
  if (!pkg.license?.includes("MPL-2.0")) return;
  const directory = path.dirname(pkg.manifest_path);
  const archivePath = path.join(
    path.dirname(path.dirname(path.dirname(directory))),
    "cache",
    path.basename(path.dirname(directory)),
    `${pkg.name}-${pkg.version}.crate`,
  );
  if (!pkg.source.startsWith("registry+") || !fs.existsSync(archivePath)) {
    throw new Error(
      `Cannot verify unmodified MPL source for ${pkg.name}; provide the modified source before distributing it`,
    );
  }
  const archive = fs.readFileSync(archivePath);
  if (sha256(archive) !== checksums.get(`${pkg.name}@${pkg.version}`))
    throw new Error(`Crate archive checksum mismatch: ${pkg.name}`);
  for (const [archiveFile, contents] of archiveFiles(archive)) {
    const prefix = `${pkg.name}-${pkg.version}/`;
    if (!archiveFile.startsWith(prefix))
      throw new Error(`Unexpected crate archive path: ${archiveFile}`);
    const file = archiveFile.slice(prefix.length);
    const absolutePath = path.join(directory, file);
    if (
      !fs.existsSync(absolutePath) ||
      sha256(fs.readFileSync(absolutePath)) !== sha256(contents)
    ) {
      throw new Error(
        `Modified MPL source: ${pkg.name}/${file}; publish the modifications and update the source notice`,
      );
    }
  }
}

function upstreamNotices(pkg, { overrides, directory, checksums }) {
  const override = overrides[`${pkg.name}@${pkg.version}`];
  if (!override) return null;
  const options =
    pkg.license?.split(/\s+OR\s+|\//).map((part) => part.trim()) || [];
  if (
    override.declared_license !== pkg.license ||
    !options.includes(override.license)
  ) {
    throw new Error(
      `Upstream license override does not match ${pkg.name}@${pkg.version}`,
    );
  }
  if (checksums.get(`${pkg.name}@${pkg.version}`) !== override.crate_sha256) {
    throw new Error(
      `Upstream override crate checksum mismatch: ${pkg.name}@${pkg.version}`,
    );
  }
  const vcsPath = path.join(
    path.dirname(pkg.manifest_path),
    ".cargo_vcs_info.json",
  );
  if (
    !fs.existsSync(vcsPath) ||
    JSON.parse(fs.readFileSync(vcsPath, "utf8")).git?.sha1 !== override.revision
  ) {
    throw new Error(
      `Upstream override revision mismatch: ${pkg.name}@${pkg.version}`,
    );
  }
  const files = override.files.map((entry) => {
    const absolutePath = path.resolve(directory, entry.file);
    const relativePath = path.relative(path.resolve(directory), absolutePath);
    if (relativePath.startsWith("..") || path.isAbsolute(relativePath)) {
      throw new Error(
        `Upstream notice is outside the override directory: ${entry.file}`,
      );
    }
    const contents = fs.readFileSync(absolutePath);
    if (sha256(contents) !== entry.sha256)
      throw new Error(`Upstream notice checksum mismatch: ${entry.file}`);
    return [
      `Upstream/${entry.file}`,
      contents.toString("utf8"),
      {
        source_url: entry.source_url,
        upstream_sha256: entry.sha256,
        ...(entry.source_path ? { source_path: entry.source_path } : {}),
      },
    ];
  });
  if (!files.length)
    throw new Error(
      `Upstream override has no notices: ${pkg.name}@${pkg.version}`,
    );
  return { files, licenseChoice: override.license };
}

function packageNotices(pkg, canonicalDirectory, upstream = { overrides: {} }) {
  if (!pkg.license && !pkg.license_file)
    throw new Error(`Missing license metadata for ${pkg.name}@${pkg.version}`);
  const directory = path.dirname(pkg.manifest_path);
  const files = noticeFiles(directory);
  // Git workspace crates can inherit the repository's license files.
  if (pkg.source.startsWith("git+")) {
    let parent = directory;
    while (!fs.existsSync(path.join(parent, ".git"))) {
      const next = path.dirname(parent);
      if (next === parent)
        throw new Error(`Cannot find Git checkout root for ${pkg.name}`);
      parent = next;
    }
    for (const entry of fs.readdirSync(parent, { withFileTypes: true })) {
      if (entry.isFile() && LICENSE_NAMES.test(entry.name)) {
        files.push([
          entry.name,
          fs.readFileSync(path.join(parent, entry.name), "utf8"),
        ]);
      }
    }
  }
  if (pkg.license_file) {
    const licensePath = path.resolve(directory, pkg.license_file);
    files.push([pkg.license_file, fs.readFileSync(licensePath, "utf8")]);
  }
  let licenseChoice = null;
  if (!files.length) {
    const verified = upstreamNotices(pkg, upstream);
    if (verified) return verified;
    // Standard attribution templates cannot replace a dependency's actual notices.
    licenseChoice = pkg.license
      .split(/\s+OR\s+|\//)
      .map((part) => part.trim())
      .find(
        (license) =>
          CANONICAL_FALLBACKS.has(license) &&
          fs.existsSync(path.join(canonicalDirectory, `${license}.txt`)),
      );
    if (!licenseChoice)
      throw new Error(
        `No license text with verified attribution for ${pkg.name}@${pkg.version}: ${pkg.license}; add a reviewed upstream override`,
      );
    files.push([
      `SPDX/${licenseChoice}.txt`,
      fs.readFileSync(
        path.join(canonicalDirectory, `${licenseChoice}.txt`),
        "utf8",
      ),
    ]);
  }
  return { files, licenseChoice };
}

function report(metadata, packageName, options) {
  const notices = new Map();
  const packages = dependencies(metadata, packageName).map((pkg) => {
    verifyMplSource(pkg, options.checksums);
    const { files, licenseChoice } = packageNotices(
      pkg,
      options.canonicalDirectory,
      options.upstream,
    );
    const packageFiles = files.map(([file, text, provenance]) => {
      const normalized = normalizeNotice(text);
      const digest = sha256(normalized);
      notices.set(digest, normalized);
      return { file, sha256: digest, ...provenance };
    });
    return {
      name: pkg.name,
      version: pkg.version,
      source: pkg.source,
      source_url: sourceUrl(pkg),
      license: pkg.license,
      ...(licenseChoice ? { license_choice: licenseChoice } : {}),
      authors: pkg.authors,
      features: metadata.resolve.nodes.find((node) => node.id === pkg.id)
        .features,
      notices: packageFiles,
    };
  });
  const inventory = {
    schema_version: 1,
    group: options.group,
    scope:
      "Conservative Cargo dependency closure, including build tools and all target-specific dependencies; not a list of only linked crates. Nested native-library notices and Rust standard-library notices are included separately.",
    cargo_lock_sha256: options.lockHash,
    rustc: options.rustc,
    rust_source_url: `https://github.com/rust-lang/rust/tree/${options.rustCommit}`,
    rust_notices_file: "RUST-NOTICES.html",
    rust_notices_sha256: sha256(options.rustNotices),
    artifacts: options.artifacts,
    packages,
  };
  const sections = packages.map(
    (pkg) => `${pkg.name} ${pkg.version}
License: ${pkg.license || "See license file"}${pkg.license_choice ? `; selected option: ${pkg.license_choice}` : ""}
Authors: ${pkg.authors.join(", ")}
Source: ${pkg.source_url}
${pkg.license?.includes("MPL-2.0") ? "This component's source is available under MPL-2.0 at the source download above. The source was verified against the published crate checksums.\n" : ""}
${pkg.notices.map((notice) => `  ${notice.file}: see Notice ${notice.sha256}${notice.source_url ? `\n    Upstream notice: ${notice.source_url}${notice.source_path ? ` (${notice.source_path})` : ""}` : ""}`).join("\n")}`,
  );
  const texts = [...notices]
    .sort(([a], [b]) => a.localeCompare(b, "en"))
    .map(([digest, text]) => `Notice ${digest}\n${"-".repeat(72)}\n${text}`);
  const text = `Third-party notices: ${packageName}
${"=".repeat(72)}

${inventory.scope}

Rust standard-library notices are supplied unchanged in RUST-NOTICES.html.
Compiler: ${options.rustc}
Exact Rust source: ${inventory.rust_source_url}

${sections.join("\n\n")}

${texts.join("\n")}
`;
  return {
    "inventory.json": `${JSON.stringify(inventory, null, 2)}\n`,
    "NOTICES.txt": normalizeNotice(text),
    "RUST-NOTICES.html": options.rustNotices,
  };
}

function settings(config, groupName, bareTarget) {
  const group = config.groups[groupName];
  if (!group) throw new Error(`Unknown binding group: ${groupName}`);
  if (group.platform === "bare") {
    if (
      !bareTarget ||
      !group.outputs.some((file) =>
        file.endsWith(
          `/${bareTarget}/buildonspark__spark-frost-bare-addon.bare`,
        ),
      )
    ) {
      throw new Error(
        "Specify a configured Bare platform directory with --bare-target",
      );
    }
    return {
      manifest: "sdks/js/packages/spark-frost-bare-addon/Cargo.toml",
      packageName: "spark-frost-bare-addon",
      output: `sdks/js/packages/spark-frost-bare-addon/prebuilds/${bareTarget}/licenses`,
      artifacts: group.outputs.filter((file) =>
        file.endsWith(
          `/${bareTarget}/buildonspark__spark-frost-bare-addon.bare`,
        ),
      ),
    };
  }
  const crateFamily =
    group.family === "token" ? "token-primitives" : group.family;
  return {
    manifest: `signer/spark-${crateFamily}-uniffi/Cargo.toml`,
    packageName: `spark-${crateFamily}-uniffi`,
    output: `sdks/js/packages/spark-sdk/third-party/${groupName}`,
    artifacts: group.outputs.filter((file) => !file.includes("/third-party/")),
  };
}

function generate(groupName, bareTarget) {
  const config = require("./sdk-bindings.json");
  const setting = settings(config, groupName, bareTarget);
  const manifest = path.join(ROOT, setting.manifest);
  const cwd = path.dirname(manifest);
  const run = (program, args) =>
    childProcess.execFileSync(program, args, {
      cwd,
      encoding: "utf8",
      maxBuffer: 64 * 1024 * 1024,
    });
  const metadata = JSON.parse(
    run("cargo", [
      "metadata",
      "--locked",
      "--format-version",
      "1",
      "--manifest-path",
      manifest,
    ]),
  );
  const rustc = run("rustc", ["--version", "--verbose"]);
  const rustCommit = /^commit-hash: ([a-f0-9]{40})$/m.exec(rustc)?.[1];
  if (!rustCommit) throw new Error("Missing Rust compiler commit hash");
  const sysroot = run("rustc", ["--print", "sysroot"]).trim();
  const rustNoticesPath = path.join(
    sysroot,
    "share/doc/rust/COPYRIGHT-library.html",
  );
  if (!fs.existsSync(rustNoticesPath))
    throw new Error(
      "Rust standard-library notices are missing; install the rust-docs component",
    );
  const artifacts = Object.fromEntries(
    setting.artifacts.map((file) => [
      file,
      sha256(fs.readFileSync(path.join(ROOT, file))),
    ]),
  );
  const output = report(metadata, setting.packageName, {
    canonicalDirectory: path.join(__dirname, "sdk-licenses/licenses"),
    upstream: {
      directory: path.join(__dirname, "sdk-licenses/upstream"),
      overrides: JSON.parse(
        fs.readFileSync(
          path.join(__dirname, "sdk-licenses/upstream/overrides.json"),
          "utf8",
        ),
      ),
      checksums: lockChecksums(
        fs.readFileSync(
          path.join(metadata.workspace_root, "Cargo.lock"),
          "utf8",
        ),
      ),
    },
    group: bareTarget ? `${groupName}/${bareTarget}` : groupName,
    lockHash: sha256(
      fs.readFileSync(path.join(metadata.workspace_root, "Cargo.lock")),
    ),
    checksums: lockChecksums(
      fs.readFileSync(path.join(metadata.workspace_root, "Cargo.lock"), "utf8"),
    ),
    rustc: rustc.split(/\r?\n/)[0],
    rustCommit,
    rustNotices: fs.readFileSync(rustNoticesPath, "utf8"),
    artifacts,
  });
  const directory = path.join(ROOT, setting.output);
  fs.mkdirSync(directory, { recursive: true });
  for (const [file, contents] of Object.entries(output))
    fs.writeFileSync(path.join(directory, file), contents);
  console.log(
    `Generated third-party notices for ${groupName}${bareTarget ? `/${bareTarget}` : ""}`,
  );
}

function verifyReports(config, groupNames = [], { root = ROOT } = {}) {
  for (const groupName of groupNames.length
    ? groupNames
    : Object.keys(config.groups)) {
    const group = config.groups[groupName];
    for (const file of group.outputs || []) {
      if (!file.endsWith("/inventory.json")) continue;
      const bareTarget =
        group.platform === "bare" ? file.split("/").at(-3) : undefined;
      const setting = settings(config, groupName, bareTarget);
      const inventory = JSON.parse(
        fs.readFileSync(path.join(root, file), "utf8"),
      );
      if (
        inventory.schema_version !== 1 ||
        inventory.rust_notices_file !== "RUST-NOTICES.html" ||
        inventory.group !==
          (bareTarget ? `${groupName}/${bareTarget}` : groupName)
      ) {
        throw new Error(`Invalid license inventory: ${file}`);
      }
      if (
        JSON.stringify(Object.keys(inventory.artifacts).sort()) !==
        JSON.stringify([...setting.artifacts].sort())
      ) {
        throw new Error(`License inventory artifact set changed: ${file}`);
      }
      for (const [artifact, expected] of Object.entries(inventory.artifacts)) {
        if (sha256(fs.readFileSync(path.join(root, artifact))) !== expected) {
          throw new Error(
            `License inventory is stale for ${artifact}; regenerate ${file}`,
          );
        }
      }
      const lockPath =
        group.platform === "bare"
          ? path.join(path.dirname(setting.manifest), "Cargo.lock")
          : "signer/Cargo.lock";
      if (
        inventory.cargo_lock_sha256 !==
        sha256(fs.readFileSync(path.join(root, lockPath)))
      ) {
        throw new Error(`License inventory lockfile changed: ${file}`);
      }
      const rustNoticesPath = path.join(
        root,
        path.dirname(file),
        inventory.rust_notices_file,
      );
      if (
        sha256(fs.readFileSync(rustNoticesPath)) !==
        inventory.rust_notices_sha256
      ) {
        throw new Error(`Rust notices do not match the inventory: ${file}`);
      }
    }
  }
}

module.exports = {
  archiveFiles,
  dependencies,
  generate,
  lockChecksums,
  noticeFiles,
  packageNotices,
  report,
  settings,
  sourceUrl,
  verifyMplSource,
  verifyReports,
};

if (require.main === module) {
  try {
    const [command, group, option, target, ...rest] = process.argv.slice(2);
    if (
      command !== "generate" ||
      !group ||
      (option && option !== "--bare-target") ||
      (option && !target) ||
      rest.length
    ) {
      throw new Error(
        "Usage: node public/scripts/sdk-licenses.js generate <binding-group> [--bare-target <platform-directory>]",
      );
    }
    generate(group, target);
  } catch (error) {
    console.error(`error: ${error.message}`);
    process.exitCode = 1;
  }
}
