const fs = require("bare-fs");
const path = require("bare-path");
const process = require("bare-process");
const { spawnSync } = require("bare-subprocess");

const packageDir = __dirname;
const testsDir = path.join(packageDir, "tests");

function run() {
  if (!fs.existsSync(testsDir)) {
    console.error(`Tests directory not found: ${testsDir}`);
    process.exit(1);
  }

  const testFiles = fs
    .readdirSync(testsDir, { withFileTypes: true })
    .filter((d) => d.isFile() && d.name.endsWith(".test.js"))
    .map((d) => d.name)
    .sort();

  if (testFiles.length === 0) {
    console.log("No test files found.");
    process.exit(0);
  }

  for (const file of testFiles) {
    const abs = path.join(testsDir, file);
    console.log(`\n=== Running: ${file} ===`);
    const res = spawnSync("bare", [abs], {
      stdio: "inherit",
      cwd: packageDir,
      env: process.env,
    });

    // If the process failed to spawn or returned non-zero, surface it and stop
    const code =
      typeof res.status === "number" ? res.status : res.signal ? 1 : 1;
    if (code !== 0) {
      console.error(`\nTest failed: ${file} (exit code ${code})`);
      process.exit(code);
    }
  }

  console.log("\nAll tests passed.");
  process.exit(0);
}

run();
