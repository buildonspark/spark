import assert from "node:assert/strict";
import { copyFileSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test, { type TestContext } from "node:test";
import { TEMPLATES } from "../templates.js";
import { transformTemplate } from "../transform.js";

function scaffoldScripts(context: TestContext, templateDir: string) {
  const targetDir = mkdtempSync(join(tmpdir(), "create-spark-app-test-"));
  context.after(() => rmSync(targetDir, { recursive: true, force: true }));

  const manifest = join(targetDir, "package.json");
  copyFileSync(
    new URL(
      `../../../../examples/${templateDir}/package.json`,
      import.meta.url,
    ),
    manifest,
  );
  transformTemplate(targetDir, "test-project");

  const { scripts } = JSON.parse(readFileSync(manifest, "utf8")) as {
    scripts: Record<string, string>;
  };
  return scripts;
}

test("the Express next step references a generated script", (context) => {
  const { dir, steps } = TEMPLATES.express;
  const scripts = scaffoldScripts(context, dir);
  const runPrefix = "{run} ";
  const step = steps.find((step) => step.startsWith(runPrefix));
  assert.ok(step, "Express should show a run command");

  const command = step.slice(runPrefix.length);
  assert.ok(
    Object.hasOwn(scripts, command),
    `Missing Express script: ${command}`,
  );
});

test("the Vite quick start references a generated script", (context) => {
  const scripts = scaffoldScripts(context, TEMPLATES.vite.dir);
  const readme = readFileSync(
    new URL("../../README.md", import.meta.url),
    "utf8",
  );
  const command = /^npm run (\S+)$/m.exec(readme)?.[1];
  assert.ok(command, "The Vite quick start should show an npm run command");
  assert.ok(Object.hasOwn(scripts, command), `Missing Vite script: ${command}`);
});
