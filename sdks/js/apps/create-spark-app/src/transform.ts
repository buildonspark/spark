import { execSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { SPARK_PACKAGES } from "./templates.js";

const DEFAULT_GITIGNORE = `node_modules
dist
build
.env
.env.local
`;

export async function transformTemplate(
  targetDir: string,
  projectName: string,
): Promise<void> {
  ensureGitignore(targetDir);
  gitInit(targetDir);

  const pkgJsonPath = join(targetDir, "package.json");

  let raw: string;
  try {
    raw = readFileSync(pkgJsonPath, "utf8");
  } catch {
    return;
  }

  const pkg = JSON.parse(raw) as Record<string, unknown>;

  pkg.name = projectName;
  delete pkg.private;
  pkg.version = "0.1.0";

  replaceDependencyVersions(pkg, "dependencies");
  replaceDependencyVersions(pkg, "devDependencies");
  replaceDependencyVersions(pkg, "peerDependencies");

  transformScripts(pkg);

  writeFileSync(pkgJsonPath, JSON.stringify(pkg, null, 2) + "\n");
}

function replaceDependencyVersions(
  pkg: Record<string, unknown>,
  field: string,
): void {
  const deps = pkg[field] as Record<string, string> | undefined;
  if (!deps) return;
  for (const name of SPARK_PACKAGES) {
    if (deps[name]) {
      deps[name] = "latest";
    }
  }
}

function transformScripts(pkg: Record<string, unknown>): void {
  const scripts = pkg.scripts as Record<string, string> | undefined;
  if (!scripts) return;

  const toDelete: string[] = [];

  for (const [key, value] of Object.entries(scripts)) {
    let updated = value;

    // Replace workspace-relative bin paths with npx
    if (updated.includes("../../node_modules/.bin/")) {
      updated = updated.replace(/\.\.\/\.\.\/node_modules\/\.bin\//g, "npx ");
    }

    // Remove scripts that reference workspace-relative config files
    if (updated.includes("CONFIG_FILE=../../")) {
      toDelete.push(key);
      continue;
    }

    scripts[key] = updated;
  }

  for (const key of toDelete) {
    delete scripts[key];
  }
}

function ensureGitignore(targetDir: string): void {
  const gitignorePath = join(targetDir, ".gitignore");
  if (existsSync(gitignorePath)) return;
  writeFileSync(gitignorePath, DEFAULT_GITIGNORE);
}

function gitInit(targetDir: string): void {
  try {
    execSync("git init", { cwd: targetDir, stdio: "ignore" });
  } catch {
    // git not installed — skip silently
  }
}
