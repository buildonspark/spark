#!/usr/bin/env node

const fs = require("fs");
const path = require("path");

// Lock file directory - same as in wallet-actions.ts
const LOCK_DIR =
  process.env.ARTILLERY_LOCK_DIR ||
  path.join(process.cwd(), ".artillery-locks");

function unlockAllWallets() {
  console.log("Unlocking all Artillery wallet locks...");
  console.log(`Lock directory: ${LOCK_DIR}`);

  if (!fs.existsSync(LOCK_DIR)) {
    console.log("No lock directory found. No locks to remove.");
    return;
  }

  const files = fs.readdirSync(LOCK_DIR);
  const lockFiles = files.filter((file) => file.endsWith(".lock"));

  if (lockFiles.length === 0) {
    console.log("No lock files found.");
    return;
  }

  console.log(`Found ${lockFiles.length} lock files.`);

  let removed = 0;
  let failed = 0;

  for (const lockFile of lockFiles) {
    const lockPath = path.join(LOCK_DIR, lockFile);
    try {
      // Read lock file to show info
      const lockContent = fs.readFileSync(lockPath, "utf-8");
      const lockData = JSON.parse(lockContent);

      console.log(`\nRemoving lock: ${lockFile}`);
      console.log(`  Pool: ${lockData.poolName || "unknown"}`);
      console.log(`  Scenario ID: ${lockData.scenarioId || "unknown"}`);
      console.log(`  Locked by: ${lockData.lockedBy || "unknown"}`);

      fs.unlinkSync(lockPath);
      removed++;
      console.log("  ✓ Removed successfully");
    } catch (error) {
      console.error(`  ✗ Failed to remove: ${error.message}`);
      failed++;
    }
  }

  console.log(`\nSummary:`);
  console.log(`  Removed: ${removed} lock files`);
  if (failed > 0) {
    console.log(`  Failed: ${failed} lock files`);
  }

  // Try to remove empty lock directory
  try {
    const remainingFiles = fs.readdirSync(LOCK_DIR);
    if (remainingFiles.length === 0) {
      fs.rmdirSync(LOCK_DIR);
      console.log(`  Removed empty lock directory`);
    }
  } catch (error) {
    // Ignore errors when removing directory
  }
}

// Parse command line arguments
const args = process.argv.slice(2);
const showHelp = args.includes("--help") || args.includes("-h");

if (showHelp) {
  console.log(`
Artillery Wallet Lock Cleaner

Usage: node unlock-wallets.js [options]

Options:
  --help, -h    Show this help message
  --dir <path>  Custom lock directory (default: ./.artillery-locks)

This utility removes all Artillery wallet lock files, which is useful when:
- Tests fail and leave wallets locked
- You need to manually clean up after interrupted tests
- You want to reset all wallet locks

Environment Variables:
  ARTILLERY_LOCK_DIR  Set custom lock directory path
`);
  process.exit(0);
}

// Check for custom directory
const dirIndex = args.findIndex((arg) => arg === "--dir");
if (dirIndex >= 0 && args[dirIndex + 1]) {
  process.env.ARTILLERY_LOCK_DIR = args[dirIndex + 1];
}

// Run the unlock
unlockAllWallets();
