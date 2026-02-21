import { existsSync, mkdirSync, rmSync } from "node:fs";
import { basename, resolve } from "node:path";
import { downloadAndExtractTemplate } from "./github.js";
import { promptTemplate } from "./prompts.js";
import { TEMPLATES, type TemplateName } from "./templates.js";
import { transformTemplate } from "./transform.js";
import { bold, cyan, dim, green, red } from "./utils.js";

declare const __PACKAGE_VERSION__: string;
const VERSION =
  typeof __PACKAGE_VERSION__ !== "undefined" ? __PACKAGE_VERSION__ : "dev";

async function main(): Promise<void> {
  const args = process.argv.slice(2);

  let projectName: string | undefined;
  let templateName: TemplateName | undefined;
  let branch: string | undefined;

  for (let i = 0; i < args.length; i++) {
    const arg = args[i]!;
    if (arg === "--template" || arg === "-t") {
      const value = args[++i];
      if (!value || !(value in TEMPLATES)) {
        console.error(red(`Invalid template: ${value ?? "(missing)"}`));
        console.error(
          `Available templates: ${Object.keys(TEMPLATES).join(", ")}`,
        );
        process.exit(1);
      }
      templateName = value as TemplateName;
    } else if (arg === "--branch" || arg === "-b") {
      branch = args[++i];
      if (!branch) {
        console.error(red("Missing branch name"));
        process.exit(1);
      }
    } else if (arg === "--version" || arg === "-V") {
      console.log(VERSION);
      process.exit(0);
    } else if (arg === "--help" || arg === "-h") {
      printHelp();
      process.exit(0);
    } else if (!arg.startsWith("-")) {
      projectName = arg;
    }
  }

  console.log();
  console.log(bold("Create Spark App"));
  console.log();

  if (!projectName) {
    const { input } = await import("@inquirer/prompts");
    projectName = await input({
      message: "Project name:",
      default: "my-spark-app",
      validate: (v: string) =>
        v.trim().length > 0 || "Project name is required",
    });
  }

  if (!templateName) {
    templateName = await promptTemplate();
  }

  const template = TEMPLATES[templateName];
  const targetDir = resolve(process.cwd(), projectName);
  const dirName = basename(targetDir);

  if (existsSync(targetDir)) {
    console.error(red(`Directory "${projectName}" already exists.`));
    process.exit(1);
  }

  console.log();
  console.log(
    `Creating ${cyan(dirName)} with template ${cyan(templateName)}...`,
  );
  console.log();

  mkdirSync(targetDir, { recursive: true });

  try {
    await downloadAndExtractTemplate(template.dir, targetDir, branch);
    await transformTemplate(targetDir, dirName);
  } catch (err) {
    rmSync(targetDir, { recursive: true, force: true });
    throw err;
  }

  const pkgManager = detectPackageManager();

  const run = pkgManager === "npm" ? "npm run" : pkgManager;

  console.log(green("Done!") + " Created " + bold(dirName));
  console.log();
  console.log("Next steps:");
  console.log(dim(`  cd ${dirName}`));
  for (const step of template.steps) {
    if (step.startsWith("#")) {
      console.log();
      console.log(`  ${step}`);
    } else {
      console.log(
        dim(`  ${step.replace("{pm}", pkgManager).replace("{run}", run)}`),
      );
    }
  }
  console.log();
}

function detectPackageManager(): string {
  const ua = process.env.npm_config_user_agent;
  if (ua) {
    if (ua.startsWith("yarn")) return "yarn";
    if (ua.startsWith("pnpm")) return "pnpm";
    if (ua.startsWith("bun")) return "bun";
  }
  return "npm";
}

function printHelp(): void {
  console.log(
    "Usage: npx @buildonspark/create-spark-app [project-name] [options]",
  );
  console.log();
  console.log("Options:");
  console.log("  --template, -t   Template to use");
  console.log(
    "  --branch, -b     Git branch to fetch templates from (default: main)",
  );
  console.log("  --version, -V    Show version");
  console.log("  --help, -h       Show help");
  console.log();
  console.log("Available templates:");
  for (const [name, { description }] of Object.entries(TEMPLATES)) {
    console.log(`  ${name.padEnd(20)} ${description}`);
  }
}

main().catch((err: unknown) => {
  console.error(
    red("Error:"),
    err instanceof Error ? err.message : String(err),
  );
  process.exit(1);
});
