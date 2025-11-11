#!/usr/bin/env node
// Build script for the Spark browser-extension example.
//
//  node build.mjs                → build for Chrome/Chromium (default)
//  node build.mjs --firefox      → build for Firefox
//  node build.mjs --all          → build for both browsers
//  node build.mjs --watch        → watch and rebuild for Chrome
//  node build.mjs --all --watch  → watch and rebuild for both browsers
//  node build.mjs --firefox --watch → watch and rebuild for Firefox
//

import { build, context as createContext } from "esbuild";
import { mkdirSync, readFileSync, writeFileSync, copyFileSync } from "node:fs";
import { argv } from "node:process";
import path from "node:path";

const watch = argv.includes("--watch");
const buildFirefox = argv.includes("--firefox");
const buildAll = argv.includes("--all");

const browsers = buildAll
  ? ["chrome", "firefox"]
  : buildFirefox
    ? ["firefox"]
    : ["chrome"];

async function buildForBrowser(browser) {
  const outdir = `dist/${browser}`;

  /** Shared esbuild options for all scripts */
  const sharedOptions = {
    bundle: true,
    format: "esm",
    platform: "browser",
    target: "es2022",
    outdir,
    sourcemap: true,
    logLevel: "info",
  };

  const backgroundOptions = {
    ...sharedOptions,
    entryPoints: ["src/background.ts"],
  };

  const contentOptions = {
    ...sharedOptions,
    entryPoints: ["src/content.ts"],
  };

  // Helper to write manifest
  const copyAssets = () => {
    // Ensure output directory exists
    mkdirSync(outdir, { recursive: true });

    // Load and customize manifest for browser
    const baseManifest = JSON.parse(readFileSync("manifest.json", "utf8"));
    const manifest = { ...baseManifest };

    // Browser-specific customizations
    if (browser === "firefox") {
      // Firefox-specific settings
      manifest.browser_specific_settings = {
        gecko: {
          id: "spark-extension@example.com",
          strict_min_version: "109.0", // Firefox 109+ has better MV3 support
        },
      };

      // Firefox uses 'scripts' field, not 'service_worker'
      manifest.background.scripts = ["background.js"];
    } else if (browser === "chrome") {
      // Chrome uses 'service_worker', not 'scripts'
      manifest.background.service_worker = "background.js";
    }

    writeFileSync(
      path.join(outdir, "manifest.json"),
      JSON.stringify(manifest, null, 2),
    );
    console.log(`✔ [${browser}] Wrote manifest.json to ${outdir}/`);
  };

  // Build scripts
  if (watch) {
    const bgOptions = {
      ...backgroundOptions,
      plugins: [
        {
          name: "copy-assets",
          setup(build) {
            build.onEnd(() => copyAssets());
          },
        },
      ],
    };
    const contentOptions = {
      ...sharedOptions,
      entryPoints: ["src/content.ts"],
    };

    const bgCtx = await createContext(bgOptions);
    const contentCtx = await createContext(contentOptions);

    // Initial asset copy
    copyAssets();

    // Return contexts for watch mode coordination
    return { bgCtx, contentCtx };
  } else {
    await Promise.all([build(backgroundOptions), build(contentOptions)]);
    copyAssets();
    return null; // No watch contexts in one-time build mode
  }
}

try {
  if (watch) {
    // Collect all watch contexts
    const watchContexts = [];
    for (const browser of browsers) {
      const contexts = await buildForBrowser(browser);
      if (contexts) {
        watchContexts.push(contexts);
      }
    }

    console.log(`\n👀 Watching for changes: ${browsers.join(", ")}`);
    console.log("Press Ctrl+C to stop\n");

    // Start watching all contexts (this keeps the process alive)
    for (const { bgCtx, contentCtx } of watchContexts) {
      await bgCtx.watch();
      await contentCtx.watch();
    }
  } else {
    // One-time build
    for (const browser of browsers) {
      await buildForBrowser(browser);
    }
    console.log(`\n✅ Build complete for: ${browsers.join(", ")}`);
  }
} catch (err) {
  console.error("Build failed", err);
  process.exit(1);
}
