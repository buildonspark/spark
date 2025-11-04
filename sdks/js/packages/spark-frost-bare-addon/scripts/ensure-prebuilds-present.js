"use strict";

const fs = require("fs");
const path = require("path");

const rootDir = path.join(__dirname, "..");

const requiredRelativeDirs = [
  "prebuilds/android-arm",
  "prebuilds/android-arm64",
  "prebuilds/android-ia32",
  "prebuilds/android-x64",
  "prebuilds/darwin-arm64",
  "prebuilds/darwin-x64",
  "prebuilds/ios-arm64",
  "prebuilds/ios-arm64-simulator",
  "prebuilds/ios-x64-simulator",
  "prebuilds/linux-arm64",
  "prebuilds/linux-x64",
  "prebuilds/win32-arm64",
  "prebuilds/win32-x64",
];

const missing = [];
for (const relDir of requiredRelativeDirs) {
  const dirPath = path.join(rootDir, relDir);
  try {
    const stat = fs.statSync(dirPath);
    if (!stat.isDirectory()) {
      missing.push(relDir + " (not a directory)");
      continue;
    }
    const entries = fs.readdirSync(dirPath);
    const bareFiles = entries.filter((name) => name.endsWith(".bare"));
    if (bareFiles.length === 0) {
      missing.push(relDir + " (no .bare files)");
      continue;
    }
    let hasNonEmpty = false;
    for (const fileName of bareFiles) {
      const filePath = path.join(dirPath, fileName);
      const fstat = fs.statSync(filePath);
      if (fstat.isFile() && fstat.size > 0) {
        hasNonEmpty = true;
        break;
      }
    }
    if (!hasNonEmpty) {
      missing.push(relDir + " (.bare files are empty)");
    }
  } catch {
    missing.push(relDir + " (missing)");
  }
}

if (missing.length > 0) {
  console.error("\nMissing required prebuild artifacts:");
  for (const m of missing) {
    console.error(" - " + m);
  }
  console.error(
    "\nBuild the prebuilds before publishing. See README.md for more details.",
  );
  process.exit(1);
}

console.log("All required prebuild artifacts are present.");
