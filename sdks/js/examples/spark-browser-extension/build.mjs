#!/usr/bin/env node
// Build script for the Spark browser-extension example.
//
//  node build.mjs              → build for Chrome/Chromium (default)
//  node build.mjs --watch ...  → rebuild on file changes
//

import { build, context as createContext } from "esbuild";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { argv } from "node:process";
import path from "node:path";

const watch = argv.includes("--watch");

const outdir = "dist/chrome";

/** Shared esbuild options */
const options = {
  entryPoints: ["src/background.ts", "src/content.ts"],
  bundle: true,
  format: "esm",
  platform: "browser",
  target: "es2022",
  outdir,
  sourcemap: true,
  logLevel: "info",
};

try {
  if (watch) {
    const ctx = await createContext(options);
    await ctx.watch();
  } else {
    await build(options);
  }

  // Ensure output directory exists.
  mkdirSync(outdir, { recursive: true });

  const manifest = JSON.parse(readFileSync("manifest.json", "utf8"));

  writeFileSync(
    path.join(outdir, "manifest.json"),
    JSON.stringify(manifest, null, 2),
  );
  console.log(`✔ Wrote manifest.json to ${outdir}/`);
} catch (err) {
  console.error("Build failed", err);
  process.exit(1);
}
