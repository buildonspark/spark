import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { spawnSync } from "node:child_process";

const VALID_SCRIPTS = new Set([
  "auto-claim-static-deposit",
  "create-lightning-invoice",
  "get-static-deposit-address",
  "get-or-create-wallet",
  "transfer",
]);

const [scriptName, ...scriptArgs] = process.argv.slice(2);

if (!scriptName || !VALID_SCRIPTS.has(scriptName)) {
  console.error(
    `Expected one of: ${Array.from(VALID_SCRIPTS).sort().join(", ")}`,
  );
  process.exit(1);
}

const env = { ...process.env };
if (env.CONFIG_FILE) {
  env.SPARK_CONFIG_JSON = readFileSync(
    resolve(process.cwd(), env.CONFIG_FILE),
    {
      encoding: "utf8",
    },
  );
}

const result = spawnSync("bare", [`${scriptName}.js`, ...scriptArgs], {
  cwd: new URL("..", import.meta.url),
  stdio: "inherit",
  env,
});

if (result.error) {
  throw result.error;
}

process.exit(result.status ?? 1);
