#!/usr/bin/env node

const childProcess = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const path = require("path");
const util = require("util");

const ROOT = path.resolve(__dirname, "../..");
const CONFIG_PATH = path.join(ROOT, "public/scripts/sdk-bindings.json");

function readJson(filePath) {
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
}

function loadConfig(configPath = CONFIG_PATH) {
  return readJson(configPath);
}

function matchesPattern(relativePath, pattern) {
  if (pattern.endsWith("/**")) {
    const prefix = pattern.slice(0, -3).replace(/\/$/, "");
    return relativePath === prefix || relativePath.startsWith(`${prefix}/`);
  }
  return relativePath === pattern;
}

function selectGroups(config, changedPaths, force = undefined) {
  const groups = config.groups;
  if (
    force === "all" ||
    changedPaths.some((relativePath) =>
      (config.force_all_triggers || []).some((pattern) =>
        matchesPattern(relativePath, pattern),
      ),
    )
  ) {
    return Object.keys(groups);
  }

  const selected = [];
  for (const [groupName, group] of Object.entries(groups)) {
    if (force && force !== group.family) {
      continue;
    }
    const patterns = [
      ...(config.common_inputs || []),
      ...(group.inputs || []),
      ...(group.trigger_inputs || []),
      ...(group.outputs || []),
    ];
    if (
      force === group.family ||
      changedPaths.some((relativePath) =>
        patterns.some((pattern) => matchesPattern(relativePath, pattern)),
      )
    ) {
      selected.push(groupName);
    }
  }
  return selected;
}

function toRepositoryPath(root, filePath) {
  return path.relative(root, filePath).split(path.sep).join("/");
}

function compareText(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

function listFiles(directory) {
  if (!fs.existsSync(directory)) {
    return [];
  }
  const stat = fs.statSync(directory);
  if (stat.isFile()) {
    return [directory];
  }
  if (!stat.isDirectory()) {
    return [];
  }

  const files = [];
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const absolutePath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...listFiles(absolutePath));
    } else if (entry.isFile()) {
      files.push(absolutePath);
    }
  }
  return files;
}

function expandPatterns(patterns, description, options = {}) {
  const root = options.root || ROOT;
  const requireMatches = options.requireMatches !== false;
  const files = new Set();

  for (const pattern of patterns) {
    const candidate = path.join(
      root,
      ...(pattern.endsWith("/**") ? pattern.slice(0, -3) : pattern).split("/"),
    );
    const matches = pattern.endsWith("/**")
      ? listFiles(candidate)
      : fs.existsSync(candidate) && fs.statSync(candidate).isFile()
        ? [candidate]
        : [];
    if (!matches.length && requireMatches) {
      throw new Error(`${description} pattern matched no files: ${pattern}`);
    }
    for (const filePath of matches) {
      files.add(filePath);
    }
  }

  return [...files].sort((left, right) =>
    compareText(toRepositoryPath(root, left), toRepositoryPath(root, right)),
  );
}

function repositoryPaths(
  root = ROOT,
  execFileSync = childProcess.execFileSync,
) {
  return execFileSync(
    "git",
    ["ls-files", "-z", "--cached", "--others", "--exclude-standard"],
    { cwd: root },
  )
    .toString()
    .split("\0")
    .filter(Boolean);
}

function expandInputPatterns(patterns, description, options = {}) {
  const root = options.root || ROOT;
  const candidates =
    options.repositoryPaths || repositoryPaths(root, options.execFileSync);
  const files = new Set();

  for (const pattern of patterns) {
    const matches = candidates.filter((relativePath) => {
      const absolutePath = path.join(root, ...relativePath.split("/"));
      return (
        matchesPattern(relativePath, pattern) &&
        fs.existsSync(absolutePath) &&
        fs.statSync(absolutePath).isFile()
      );
    });
    if (!matches.length) {
      throw new Error(`${description} pattern matched no files: ${pattern}`);
    }
    for (const relativePath of matches) {
      files.add(path.join(root, ...relativePath.split("/")));
    }
  }

  return [...files].sort((left, right) =>
    compareText(toRepositoryPath(root, left), toRepositoryPath(root, right)),
  );
}

function digest(content) {
  return crypto.createHash("sha256").update(content).digest("hex");
}

function fileHash(filePath) {
  return digest(fs.readFileSync(filePath));
}

function sourceConfigJson(value) {
  if (Array.isArray(value)) {
    return `[${value.map(sourceConfigJson).join(", ")}]`;
  }
  if (value && typeof value === "object") {
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${JSON.stringify(key)}: ${sourceConfigJson(value[key])}`)
      .join(", ")}}`;
  }
  return JSON.stringify(value);
}

function canonicalJson(value, relativePath = "") {
  if (Array.isArray(value)) {
    return `[${value.map((item) => canonicalJson(item, relativePath)).join(",")}]`;
  }
  if (value && typeof value === "object") {
    return `{${Object.keys(value)
      .sort()
      .map(
        (key) =>
          `${JSON.stringify(key)}:${canonicalJson(value[key], relativePath)}`,
      )
      .join(",")}}`;
  }
  if (typeof value === "number" && !Number.isSafeInteger(value)) {
    throw new Error(
      `Transformed JSON numbers must be safe integers: ${relativePath}`,
    );
  }
  return JSON.stringify(value);
}

function sourceFileDigest(config, relativePath, content) {
  const transform = (config.source_hash_transforms || {})[relativePath];
  if (!transform) {
    return digest(content);
  }

  const unsupported = Object.keys(transform).filter(
    (key) => key !== "remove_top_level_json_fields",
  );
  if (unsupported.length) {
    throw new Error(
      `Unsupported source hash transform for ${relativePath}: ${unsupported.sort().join(", ")}`,
    );
  }
  const fields = transform.remove_top_level_json_fields || [];
  if (!fields.length || !fields.every((field) => typeof field === "string")) {
    throw new Error(`Invalid JSON field exclusions for ${relativePath}`);
  }

  const value = JSON.parse(content.toString("utf8"));
  if (!value || Array.isArray(value) || typeof value !== "object") {
    throw new Error(
      `Source hash transform requires a JSON object: ${relativePath}`,
    );
  }
  for (const field of fields) {
    delete value[field];
  }
  return digest(canonicalJson(value, relativePath));
}

function sourceFileHash(config, filePath, options = {}) {
  const root = options.root || ROOT;
  const relativePath = toRepositoryPath(root, filePath);
  return sourceFileDigest(config, relativePath, fs.readFileSync(filePath));
}

function sourceDigest(config, groupName, paths, fileHashes) {
  const hash = crypto.createHash("sha256");
  hash.update("spark-sdk-bindings-v1\0");
  hash.update(groupName);
  hash.update("\0");
  hash.update(sourceConfigJson(config.tool_versions || {}));
  hash.update("\0");
  for (const relativePath of paths) {
    hash.update(relativePath);
    hash.update("\0");
    hash.update(Buffer.from(fileHashes.get(relativePath), "hex"));
  }
  return hash.digest("hex");
}

function configuredOutputPaths(config) {
  return new Set(
    Object.values(config.groups).flatMap((group) => group.outputs || []),
  );
}

function managedOutputPatterns(config) {
  const outputs = configuredOutputPaths(config);
  const patterns = config.managed_output_patterns || [...outputs].sort();
  for (const output of outputs) {
    if (!patterns.some((pattern) => matchesPattern(output, pattern))) {
      throw new Error(
        `Configured output is outside managed output patterns: ${output}`,
      );
    }
  }
  return patterns;
}

function managedOutputPaths(config, options = {}) {
  return expandPatterns(managedOutputPatterns(config), "managed output", {
    ...options,
    requireMatches: false,
  });
}

function sourceHash(config, groupName, options = {}) {
  const root = options.root || ROOT;
  const group = config.groups[groupName];
  const paths = expandInputPatterns(
    [...(config.common_inputs || []), ...(group.inputs || [])],
    `${groupName} input`,
    options,
  );
  const relativePaths = paths.map((filePath) =>
    toRepositoryPath(root, filePath),
  );
  const hashes = new Map(
    paths.map((filePath, index) => [
      relativePaths[index],
      sourceFileHash(config, filePath, { root }),
    ]),
  );
  return [sourceDigest(config, groupName, relativePaths, hashes), paths.length];
}

function artifactHashes(config, groupName, options = {}) {
  const root = options.root || ROOT;
  const paths = expandPatterns(
    config.groups[groupName].outputs,
    `${groupName} output`,
    options,
  );
  return Object.fromEntries(
    paths.map((filePath) => [
      toRepositoryPath(root, filePath),
      fileHash(filePath),
    ]),
  );
}

function manifestPath(config, options = {}) {
  return path.join(options.root || ROOT, ...config.manifest.split("/"));
}

function readManifest(config, options = {}) {
  const filePath = manifestPath(config, options);
  if (!fs.existsSync(filePath)) {
    return { schema_version: config.schema_version, groups: {} };
  }
  return readJson(filePath);
}

function resolveGroups(config, groupNames = []) {
  if (!groupNames.length) {
    return Object.keys(config.groups);
  }
  const unknown = [...new Set(groupNames)]
    .filter((name) => !(name in config.groups))
    .sort();
  if (unknown.length) {
    throw new Error(`Unknown binding groups: ${unknown.join(", ")}`);
  }
  return groupNames;
}

function sortJson(value) {
  if (Array.isArray(value)) {
    return value.map(sortJson);
  }
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, sortJson(value[key])]),
    );
  }
  return value;
}

function sameMembers(left, right) {
  const leftSet = new Set(left);
  const rightSet = new Set(right);
  return (
    leftSet.size === rightSet.size &&
    [...leftSet].every((item) => rightSet.has(item))
  );
}

function updateManifest(config, groupNames = [], options = {}) {
  const root = options.root || ROOT;
  let manifest = readManifest(config, options);
  if (manifest.schema_version !== config.schema_version) {
    manifest = { schema_version: config.schema_version, groups: {} };
  }
  const groups = resolveGroups(config, groupNames);
  if (sameMembers(groups, Object.keys(config.groups))) {
    manifest.groups = {};
  }
  for (const groupName of groups) {
    const [inputDigest, inputCount] = sourceHash(config, groupName, options);
    manifest.groups[groupName] = {
      input_sha256: inputDigest,
      input_file_count: inputCount,
      artifacts: artifactHashes(config, groupName, options),
    };
  }
  const filePath = manifestPath(config, options);
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(
    filePath,
    `${JSON.stringify(sortJson(manifest), null, 2)}\n`,
  );
  console.log(
    `Updated ${toRepositoryPath(root, filePath)} for ${groups.join(", ")}`,
  );
}

function difference(left, right) {
  return [...left].filter((item) => !right.has(item)).sort();
}

function checkManifest(config, groupNames = [], options = {}) {
  const root = options.root || ROOT;
  const filePath = manifestPath(config, options);
  if (!fs.existsSync(filePath)) {
    throw new Error(
      `Binding manifest is missing: ${toRepositoryPath(root, filePath)}`,
    );
  }
  const manifest = options.manifest || readManifest(config, options);
  if (manifest.schema_version !== config.schema_version) {
    throw new Error(
      "Binding manifest schema does not match the binding config",
    );
  }

  const computeSourceHash =
    options.sourceHash ||
    ((groupName) => sourceHash(config, groupName, options));
  const computeArtifactHashes =
    options.artifactHashes ||
    ((groupName) => artifactHashes(config, groupName, options));
  const errors = [];
  for (const field of difference(
    new Set(Object.keys(manifest)),
    new Set(["schema_version", "groups"]),
  )) {
    errors.push(`unexpected manifest field: ${field}`);
  }
  try {
    const managedPaths = new Set(
      managedOutputPaths(config, options).map((outputPath) =>
        toRepositoryPath(root, outputPath),
      ),
    );
    for (const unexpected of difference(
      managedPaths,
      configuredOutputPaths(config),
    )) {
      errors.push(`unconfigured generated artifact: ${unexpected}`);
    }
  } catch (error) {
    errors.push(error.message);
  }

  const manifestGroups = manifest.groups || {};
  for (const groupName of difference(
    new Set(Object.keys(manifestGroups)),
    new Set(Object.keys(config.groups)),
  )) {
    errors.push(`unexpected manifest group: ${groupName}`);
  }
  for (const groupName of resolveGroups(config, groupNames)) {
    const recorded = manifestGroups[groupName];
    if (!recorded) {
      errors.push(`${groupName}: no manifest entry`);
      continue;
    }
    for (const field of difference(
      new Set(Object.keys(recorded)),
      new Set(["input_sha256", "input_file_count", "artifacts"]),
    )) {
      errors.push(`${groupName}: unexpected manifest field ${field}`);
    }
    const [currentInputHash, currentInputCount] = computeSourceHash(groupName);
    if (recorded.input_sha256 !== currentInputHash) {
      errors.push(`${groupName}: source inputs changed`);
    }
    if (recorded.input_file_count !== currentInputCount) {
      errors.push(`${groupName}: source input file set changed`);
    }

    let currentArtifacts;
    try {
      currentArtifacts = computeArtifactHashes(groupName);
    } catch (error) {
      errors.push(error.message);
      continue;
    }
    const recordedArtifacts = recorded.artifacts || {};
    if (!util.isDeepStrictEqual(recordedArtifacts, currentArtifacts)) {
      const recordedPaths = new Set(Object.keys(recordedArtifacts));
      const currentPaths = new Set(Object.keys(currentArtifacts));
      for (const missing of difference(recordedPaths, currentPaths)) {
        errors.push(`${groupName}: missing artifact ${missing}`);
      }
      for (const added of difference(currentPaths, recordedPaths)) {
        errors.push(`${groupName}: unrecorded artifact ${added}`);
      }
      for (const changed of [...recordedPaths]
        .filter((item) => currentPaths.has(item))
        .sort()) {
        if (recordedArtifacts[changed] !== currentArtifacts[changed]) {
          errors.push(`${groupName}: artifact changed ${changed}`);
        }
      }
    }
  }

  if (errors.length) {
    throw new Error(
      `Committed SDK bindings are stale:\n  - ${errors.join("\n  - ")}`,
    );
  }
  console.log(
    "SDK binding manifest matches all configured inputs and artifacts.",
  );
}

function cleanOutputs(config, groupNames = [], options = {}) {
  const groups = resolveGroups(config, groupNames);
  const allGroups = Object.keys(config.groups);
  const paths = new Set();
  if (sameMembers(groups, allGroups)) {
    for (const outputPath of managedOutputPaths(config, options)) {
      paths.add(outputPath);
    }
  } else {
    for (const groupName of groups) {
      for (const outputPath of expandPatterns(
        config.groups[groupName].outputs,
        `${groupName} output`,
        { ...options, requireMatches: false },
      )) {
        paths.add(outputPath);
      }
    }
  }
  for (const outputPath of paths) {
    fs.rmSync(outputPath);
  }
  console.log(
    `Removed ${paths.size} generated binding files before artifact extraction.`,
  );
}

function createArchive(config, groupNames, output, options = {}) {
  const root = options.root || ROOT;
  const groups = resolveGroups(config, groupNames);
  const selectedOutputs = groups.flatMap(
    (groupName) => config.groups[groupName].outputs || [],
  );
  const selectedManagedPatterns = managedOutputPatterns(config).filter(
    (pattern) =>
      selectedOutputs.some((relativePath) =>
        matchesPattern(relativePath, pattern),
      ),
  );
  const configuredOutputs = configuredOutputPaths(config);
  const undeclaredOutputs = expandPatterns(
    selectedManagedPatterns,
    "selected managed output",
    { ...options, requireMatches: false },
  )
    .map((filePath) => toRepositoryPath(root, filePath))
    .filter((relativePath) => !configuredOutputs.has(relativePath));
  if (undeclaredOutputs.length) {
    throw new Error(
      `Binding producer emitted undeclared outputs:\n  - ${undeclaredOutputs.join("\n  - ")}`,
    );
  }
  const files = new Set();
  for (const groupName of groups) {
    for (const filePath of expandPatterns(
      config.groups[groupName].outputs,
      `${groupName} output`,
      options,
    )) {
      files.add(toRepositoryPath(root, filePath));
    }
  }
  const outputPath = path.resolve(output);
  fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  const sortedFiles = [...files].sort();
  const execFileSync = options.execFileSync || childProcess.execFileSync;
  const tarEnvironment = { ...process.env, COPYFILE_DISABLE: "1" };
  execFileSync("tar", ["-czf", outputPath, "-C", root, ...sortedFiles], {
    stdio: "inherit",
    env: tarEnvironment,
  });
  const archiveFiles = execFileSync("tar", ["-tzf", outputPath], {
    encoding: "utf8",
    env: tarEnvironment,
  })
    .split(/\r?\n/)
    .filter(Boolean)
    .sort(compareText);
  if (!util.isDeepStrictEqual(archiveFiles, sortedFiles)) {
    throw new Error(
      `Binding archive contains unexpected paths: ${archiveFiles.join(", ")}`,
    );
  }
  console.log(
    `Archived ${files.size} files for ${groups.join(", ")} to ${outputPath}`,
  );
}

function stageUpdate(config, output, options = {}) {
  const root = options.root || ROOT;
  const outputPath = path.resolve(output);
  if (fs.existsSync(outputPath) && fs.readdirSync(outputPath).length) {
    throw new Error(`Binding update directory is not empty: ${outputPath}`);
  }

  const files = new Set([manifestPath(config, options)]);
  for (const [groupName, group] of Object.entries(config.groups)) {
    for (const filePath of expandPatterns(
      group.outputs,
      `${groupName} output`,
      options,
    )) {
      files.add(filePath);
    }
  }
  const sortedFiles = [...files].sort((left, right) =>
    compareText(toRepositoryPath(root, left), toRepositoryPath(root, right)),
  );
  for (const source of sortedFiles) {
    const destination = path.join(outputPath, path.relative(root, source));
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.copyFileSync(source, destination);
  }
  console.log(`Staged ${files.size} generated binding files in ${outputPath}`);
}

function changedPaths(base, head, options = {}) {
  if (!base || /^0+$/.test(base)) {
    return [];
  }
  return (options.execFileSync || childProcess.execFileSync)(
    "git",
    ["diff", "--no-renames", "--name-only", "--diff-filter=ACMRD", base, head],
    { cwd: options.root || ROOT, encoding: "utf8" },
  )
    .split(/\r?\n/)
    .filter(Boolean);
}

function writeDetectionOutputs(config, selected, outputPath) {
  const selectedSet = new Set(selected);
  const values = [];
  for (const groupName of Object.keys(config.groups)) {
    values.push([
      groupName.replaceAll("-", "_"),
      String(selectedSet.has(groupName)),
    ]);
  }
  for (const platform of ["wasm", "android", "ios", "bare"]) {
    values.push([
      platform,
      String(
        Object.entries(config.groups).some(
          ([groupName, group]) =>
            selectedSet.has(groupName) && group.platform === platform,
        ),
      ),
    ]);
  }
  values.push(["any", String(selected.length > 0)]);
  values.push(["groups", selected.join(",")]);
  fs.appendFileSync(
    outputPath,
    values.map(([key, value]) => `${key}=${value}\n`).join(""),
  );
}

function detectCommand(config, args, options = {}) {
  const getChangedPaths = options.changedPaths || changedPaths;
  const chooseGroups = options.selectGroups || selectGroups;
  const log = options.log || console.log;
  const paths = getChangedPaths(args.base, args.head, options);
  let force = args.force;
  if ((!args.base || /^0+$/.test(args.base)) && !force) {
    force = "all";
  }
  const selected = chooseGroups(config, paths, force);
  log("Changed paths:");
  for (const relativePath of paths) {
    log(`  ${relativePath}`);
  }
  log(`Selected binding groups: ${selected.join(", ") || "none"}`);
  if (args.githubOutput) {
    writeDetectionOutputs(config, selected, args.githubOutput);
  }
  const summaryPath = (options.env || process.env).GITHUB_STEP_SUMMARY;
  if (summaryPath) {
    const summary =
      selected.map((group) => `\`${group}\``).join(", ") || "No groups";
    fs.appendFileSync(summaryPath, `## SDK binding selection\n\n${summary}\n`);
  }
  return selected;
}

function verifyPackages(config, options = {}) {
  const root = options.root || ROOT;
  (options.checkManifest || checkManifest)(config, [], options);
  const manifest = options.manifest || readManifest(config, options);
  const expectedByPackage = new Map(
    Object.keys(config.packages).map((packageName) => [packageName, new Set()]),
  );

  for (const groupName of Object.keys(config.groups)) {
    for (const artifactPath of Object.keys(
      manifest.groups[groupName].artifacts,
    )) {
      let matchedPackage = false;
      for (const [packageName, packageRoot] of Object.entries(
        config.packages,
      )) {
        const prefix = `${packageRoot.replace(/\/$/, "")}/`;
        if (artifactPath.startsWith(prefix)) {
          expectedByPackage
            .get(packageName)
            .add(artifactPath.slice(prefix.length));
          matchedPackage = true;
          break;
        }
      }
      if (!matchedPackage) {
        throw new Error(
          `No npm package owns generated artifact ${artifactPath}`,
        );
      }
    }
  }

  for (const [packageName, packageRoot] of Object.entries(config.packages)) {
    const report =
      options.packageReports?.[packageName] ||
      JSON.parse(
        (options.execFileSync || childProcess.execFileSync)(
          "npm",
          ["pack", "--dry-run", "--json", "--ignore-scripts"],
          { cwd: path.join(root, ...packageRoot.split("/")), encoding: "utf8" },
        ),
      );
    const packedFiles = new Set(report[0].files.map((entry) => entry.path));
    const missing = difference(expectedByPackage.get(packageName), packedFiles);
    if (missing.length) {
      throw new Error(
        `${packageName} omits generated binding artifacts:\n  - ${missing.join("\n  - ")}`,
      );
    }
    console.log(
      `${packageName} dry-run package contains all ${expectedByPackage.get(packageName).size} generated binding files.`,
    );
  }
}

function packageVersion(packageName, options = {}) {
  const root = options.root || ROOT;
  const searchPaths = [
    path.join(root, "sdks/js/packages/spark-frost-bare-addon"),
    path.join(root, "sdks/js"),
  ];
  let manifestPath;
  try {
    manifestPath = require.resolve(`${packageName}/package.json`, {
      paths: searchPaths,
    });
  } catch (error) {
    if (
      error.code !== "ERR_PACKAGE_PATH_NOT_EXPORTED" &&
      error.code !== "MODULE_NOT_FOUND"
    ) {
      throw error;
    }
    let directory = path.dirname(
      require.resolve(packageName, { paths: searchPaths }),
    );
    while (directory !== path.dirname(directory)) {
      const candidate = path.join(directory, "package.json");
      if (fs.existsSync(candidate)) {
        const manifest = readJson(candidate);
        if (manifest.name === packageName) {
          manifestPath = candidate;
          break;
        }
      }
      directory = path.dirname(directory);
    }
  }
  if (!manifestPath) {
    throw new Error(`Cannot locate installed Bare build tool ${packageName}`);
  }
  return readJson(manifestPath).version;
}

function verifyBareToolVersions(config, options = {}) {
  const expected = config.bare_tool_versions || {};
  if (!Object.keys(expected).length) {
    throw new Error("No Bare build tool versions are configured");
  }
  const installed = options.installedVersions || {};
  const errors = [];
  for (const [packageName, expectedVersion] of Object.entries(expected)) {
    const installedVersion =
      installed[packageName] || packageVersion(packageName, options);
    if (installedVersion !== expectedVersion) {
      errors.push(
        `${packageName}: expected ${expectedVersion}, got ${installedVersion}`,
      );
    }
  }
  if (errors.length) {
    throw new Error(
      `Bare build tool versions changed:\n  - ${errors.join("\n  - ")}`,
    );
  }
  console.log(
    `Bare build tool versions match ${Object.keys(expected).length} configured packages.`,
  );
}

function optionValue(tokens, index, name) {
  const value = tokens[index + 1];
  if (value === undefined || value.startsWith("--")) {
    throw new Error(`${name} requires a value`);
  }
  return value;
}

function parseArgs(argv) {
  const tokens = [...argv];
  const command = tokens.shift();
  const commands = new Set([
    "detect",
    "update",
    "check",
    "clean",
    "archive",
    "stage",
    "verify-bare-tools",
    "verify-packages",
  ]);
  if (!commands.has(command)) {
    throw new Error(`Unknown or missing command: ${command || "(none)"}`);
  }

  const args = { command, head: "HEAD", groups: [] };
  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index];
    if (token === "--groups") {
      while (
        tokens[index + 1] !== undefined &&
        !tokens[index + 1].startsWith("--")
      ) {
        args.groups.push(tokens[index + 1]);
        index += 1;
      }
    } else if (token === "--base") {
      args.base = optionValue(tokens, index, token);
      index += 1;
    } else if (token === "--head") {
      args.head = optionValue(tokens, index, token);
      index += 1;
    } else if (token === "--force") {
      args.force = optionValue(tokens, index, token);
      index += 1;
    } else if (token === "--github-output") {
      args.githubOutput = optionValue(tokens, index, token);
      index += 1;
    } else if (token === "--output") {
      args.output = optionValue(tokens, index, token);
      index += 1;
    } else {
      throw new Error(`Unknown argument: ${token}`);
    }
  }

  if (args.force && !["all", "frost", "token"].includes(args.force)) {
    throw new Error(`Invalid --force value: ${args.force}`);
  }
  if (command === "archive" && (!args.groups.length || !args.output)) {
    throw new Error("archive requires --groups and --output");
  }
  if (command === "stage" && !args.output) {
    throw new Error("stage requires --output");
  }
  return args;
}

function main(argv = process.argv.slice(2)) {
  const args = parseArgs(argv);
  const config = loadConfig();
  if (args.command === "detect") {
    detectCommand(config, args);
  } else if (args.command === "update") {
    updateManifest(config, args.groups);
  } else if (args.command === "check") {
    checkManifest(config, args.groups);
  } else if (args.command === "clean") {
    cleanOutputs(config, args.groups);
  } else if (args.command === "archive") {
    createArchive(config, args.groups, args.output);
  } else if (args.command === "stage") {
    stageUpdate(config, args.output);
  } else if (args.command === "verify-bare-tools") {
    verifyBareToolVersions(config);
  } else if (args.command === "verify-packages") {
    verifyPackages(config);
  }
}

module.exports = {
  artifactHashes,
  canonicalJson,
  changedPaths,
  checkManifest,
  cleanOutputs,
  configuredOutputPaths,
  createArchive,
  detectCommand,
  expandInputPatterns,
  expandPatterns,
  fileHash,
  loadConfig,
  managedOutputPaths,
  managedOutputPatterns,
  matchesPattern,
  parseArgs,
  readManifest,
  resolveGroups,
  selectGroups,
  sourceDigest,
  sourceFileDigest,
  sourceFileHash,
  sourceHash,
  stageUpdate,
  updateManifest,
  verifyBareToolVersions,
  verifyPackages,
  writeDetectionOutputs,
};

if (require.main === module) {
  try {
    main();
  } catch (error) {
    console.error(`error: ${error.message}`);
    process.exitCode = 1;
  }
}
