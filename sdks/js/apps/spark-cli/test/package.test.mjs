import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const npmCommand = process.platform === "win32" ? "npm.cmd" : "npm";

function run(command, args, cwd, { isolated = false } = {}) {
  const environment = {
    ...process.env,
    NO_COLOR: "1",
  };
  if (isolated) {
    delete environment.NODE_OPTIONS;
    environment.PATH = [dirname(process.execPath), environment.PATH]
      .filter(Boolean)
      .join(delimiter);
  }

  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    env: environment,
    shell: process.platform === "win32",
  });

  assert.equal(
    result.status,
    0,
    [result.stdout, result.stderr].filter(Boolean).join("\n"),
  );
  return result;
}

test("the packed CLI installs and starts", { timeout: 120_000 }, () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "spark-cli-package-"));
  const packDirectory = join(temporaryRoot, "pack");
  const installDirectory = join(temporaryRoot, "install");

  try {
    mkdirSync(packDirectory);
    rmSync(join(packageRoot, "dist"), { force: true, recursive: true });
    run(npmCommand, ["pack", "--pack-destination", packDirectory], packageRoot);

    const tarballs = readdirSync(packDirectory).filter((file) =>
      file.endsWith(".tgz"),
    );
    assert.equal(tarballs.length, 1);

    run(
      npmCommand,
      [
        "install",
        "--ignore-scripts",
        "--no-audit",
        "--no-fund",
        "--prefix",
        installDirectory,
        join(packDirectory, tarballs[0]),
      ],
      temporaryRoot,
      { isolated: true },
    );

    const installedPackage = join(
      installDirectory,
      "node_modules",
      "@buildonspark",
      "cli",
    );
    const manifest = JSON.parse(
      readFileSync(join(installedPackage, "package.json"), "utf8"),
    );
    const binTarget = manifest.bin?.["spark-cli"];
    assert.equal(typeof binTarget, "string");

    const installedExecutable = resolve(installedPackage, binTarget);
    assert.ok(existsSync(installedExecutable));
    if (process.platform !== "win32") {
      assert.notEqual(statSync(installedExecutable).mode & 0o111, 0);
    }

    const installedBin = join(
      installDirectory,
      "node_modules",
      ".bin",
      process.platform === "win32" ? "spark-cli.cmd" : "spark-cli",
    );
    const result = run(installedBin, ["--help"], temporaryRoot, {
      isolated: true,
    });
    assert.match(result.stdout, /Usage: spark-cli \[options\]/);
  } finally {
    rmSync(temporaryRoot, { force: true, recursive: true });
  }
});
