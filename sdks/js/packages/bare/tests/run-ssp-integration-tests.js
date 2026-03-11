require("bare-node-runtime/global");
const fs = require("bare-fs");
const path = require("bare-path");
const { spawnSync } = require("bare-subprocess");

const packageDir = path.resolve(__dirname, "..");
const testsDir = path.join(__dirname, "integration", "ssp");

const TIMEOUT_MS = 120_000; // 2 minutes per test file

function run() {
  if (!fs.existsSync(testsDir)) {
    console.error(`SSP tests directory not found: ${testsDir}`);
    process.exit(1);
  }

  const testFiles = fs
    .readdirSync(testsDir, { withFileTypes: true })
    .filter((d) => d.isFile() && d.name.endsWith(".test.js"))
    .map((d) => d.name)
    .sort();

  if (testFiles.length === 0) {
    console.log("No SSP integration test files found.");
    process.exit(0);
  }

  let passed = 0;
  let failed = 0;

  for (const file of testFiles) {
    const abs = path.join(testsDir, file);
    console.log(`\n=== Running: ssp/${file} ===`);
    const res = spawnSync("bare", [abs], {
      stdio: "inherit",
      cwd: packageDir,
      env: process.env,
      timeout: TIMEOUT_MS,
    });

    const code = typeof res.status === "number" ? res.status : 1;
    if (code !== 0) {
      console.error(`\nFAIL: ssp/${file} (exit code ${code})`);
      failed++;
      if (process.env.GITHUB_ACTIONS) {
        process.exit(code);
      }
    } else {
      passed++;
    }
  }

  console.log(
    `\n${passed} passed, ${failed} failed out of ${testFiles.length} SSP test files.`,
  );
  process.exit(failed > 0 ? 1 : 0);
}

run();
