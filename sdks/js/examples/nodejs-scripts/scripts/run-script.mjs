import { spawnSync } from "node:child_process";

const VALID_SCRIPTS = new Set([
  "example",
  "get-or-create-wallet",
  "create-invoice",
  "deposit-bitcoin",
  "get-all-transfers",
  "get-balance",
  "get-spark-address",
  "get-transfers-with-time-filter",
  "pay-invoices",
  "send-transfer",
]);

const [scriptName, ...scriptArgs] = process.argv.slice(2);

if (!scriptName || !VALID_SCRIPTS.has(scriptName)) {
  console.error(
    `Expected one of: ${Array.from(VALID_SCRIPTS).sort().join(", ")}`,
  );
  process.exit(1);
}

const result = spawnSync(
  "tsx",
  [`./src/spark-sdk/${scriptName}.ts`, ...scriptArgs],
  {
    cwd: new URL("..", import.meta.url),
    stdio: "inherit",
    env: process.env,
  },
);

if (result.error) {
  throw result.error;
}

process.exit(result.status ?? 1);
