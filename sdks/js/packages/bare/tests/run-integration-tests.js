require("bare-node-runtime/global");
const fs = require("bare-fs");
const path = require("bare-path");
const { spawnSync } = require("bare-subprocess");

const packageDir = path.resolve(__dirname, "..");
const testsDir = path.join(__dirname, "integration");

// bare-subprocess.spawnSync does not support the `timeout` option (it is silently
// ignored).  Use the Unix `timeout` command instead, which also sends SIGKILL
// (-k) if the process is still alive 5 s after the initial SIGTERM.
const TIMEOUT_SECS = 120; // 2 minutes per test file

function run() {
  if (!process.env.SPARK_LOCAL_INGRESS_HOST) {
    console.error(
      "SPARK_LOCAL_INGRESS_HOST not set. Integration tests require hermetic environment.",
    );
    process.exit(1);
  }

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
    console.log("No integration test files found.");
    process.exit(0);
  }

  let passed = 0;
  let failed = 0;

  for (const file of testFiles) {
    const abs = path.join(testsDir, file);
    console.log(`\n=== Running: ${file} ===`);
    // Wrap with `timeout -k 5s <N>s` so the child is forcefully killed
    // (SIGKILL) if it does not exit within 5 s of the initial SIGTERM.
    // Exit code 124 from `timeout` means the command timed out.
    const res = spawnSync(
      "timeout",
      ["-k", "5s", `${TIMEOUT_SECS}s`, "bare", abs],
      {
        stdio: "inherit",
        cwd: packageDir,
        env: process.env,
      },
    );

    const code = typeof res.status === "number" ? res.status : 1;
    if (code !== 0) {
      if (code === 124) {
        console.error(`\nFAIL: ${file} (timed out after ${TIMEOUT_SECS}s)`);
      } else {
        console.error(`\nFAIL: ${file} (exit code ${code})`);
      }
      failed++;
      if (process.env.GITHUB_ACTIONS) {
        process.exit(code);
      }
    } else {
      passed++;
    }
  }

  console.log(
    `\n${passed} passed, ${failed} failed out of ${testFiles.length} test files.`,
  );
  process.exit(failed > 0 ? 1 : 0);
}

run();
