import path from "node:path";
import { defineConfig, devices } from "@playwright/test";

const baseURL = "http://127.0.0.1:4173";
const outputDir =
  process.env["HERMETIC_TEST"] === "true" && process.env["GITHUB_WORKSPACE"]
    ? path.join(
        process.env["GITHUB_WORKSPACE"],
        "logs/playwright/spark-vite-app",
      )
    : "../../../../output/playwright/spark-vite-app";

export default defineConfig({
  testDir: "./playwright",
  fullyParallel: false,
  workers: 1,
  timeout: 180_000,
  expect: {
    timeout: 10_000,
  },
  reporter: "list",
  outputDir,
  use: {
    baseURL,
    permissions: ["clipboard-read", "clipboard-write"],
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    video: "on",
  },
  projects: [
    {
      name: "chromium",
      use: devices["Desktop Chrome"],
    },
  ],
  webServer: {
    command: "yarn start --force --host 127.0.0.1 --port 4173",
    env: {
      CONFIG_FILE: "playwright/test-wallet-config.json",
    },
    url: baseURL,
    reuseExistingServer: !process.env["GITHUB_ACTIONS"],
    timeout: 180_000,
  },
});
