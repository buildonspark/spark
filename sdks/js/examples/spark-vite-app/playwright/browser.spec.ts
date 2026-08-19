import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";
import {
  installWasmRecorder,
  instantiatedWasmHashes,
  loadApp,
} from "./browser-test-utils.js";

const FROST_WASM_PATH = fileURLToPath(
  new URL(
    "../../../packages/spark-sdk/src/spark-bindings/wasm/wasm-browser-bg.wasm",
    import.meta.url,
  ),
);
const TOKEN_WASM_PATH = fileURLToPath(
  new URL(
    "../../../packages/spark-sdk/src/token-primitives-bindings/wasm/wasm-browser-bg.wasm",
    import.meta.url,
  ),
);
const PLAYWRIGHT_DRIVER_URL = "/playwright/browser-driver.ts";

type BrowserDriver = {
  runFrostBindingChecks(): Promise<{
    availableMethods: string[];
    calledMethods: string[];
  }>;
  runTokenBindingChecks(): Promise<{ manifestHash: string }>;
  runWalletJourney(): Promise<{
    restoredAddress: string;
    transferId: string;
    receiverBalance: string;
  }>;
  runTokenLifecycle(): Promise<{
    tokenIdentifier: string;
    transactionId: string;
    receiverBalance: string;
  }>;
};

async function sha256(filePath: string): Promise<string> {
  return createHash("sha256")
    .update(await readFile(filePath))
    .digest("hex");
}

test.beforeEach(async ({ context, page }) => {
  await installWasmRecorder(context);
  await loadApp(page);
});

test("exercises every FROST browser binding with the installed WASM @bindings", async ({
  page,
}) => {
  const result = await page.evaluate(async (driverUrl) => {
    const driver = (await import(driverUrl)) as BrowserDriver;
    return driver.runFrostBindingChecks();
  }, PLAYWRIGHT_DRIVER_URL);

  expect(result.availableMethods).toHaveLength(17);
  expect(result.calledMethods).toEqual(result.availableMethods);
  const expectedHash = await sha256(FROST_WASM_PATH);
  expect(await instantiatedWasmHashes(page)).toContain(expectedHash);
  test.info().annotations.push({
    type: "binding-provenance",
    description: `FROST ${expectedHash}`,
  });
});

test("hashes and signs a transfer manifest with the installed token WASM @bindings", async ({
  page,
}) => {
  const result = await page.evaluate(async (driverUrl) => {
    const driver = (await import(driverUrl)) as BrowserDriver;
    return driver.runTokenBindingChecks();
  }, PLAYWRIGHT_DRIVER_URL);

  expect(result.manifestHash).toBe(
    "56a23f58799776190d566f70d88d8c3be8db059a598c8932b3250a8076abbacd",
  );
  const expectedHash = await sha256(TOKEN_WASM_PATH);
  expect(await instantiatedWasmHashes(page)).toContain(expectedHash);
  test.info().annotations.push({
    type: "binding-provenance",
    description: `token primitives ${expectedHash}`,
  });
});

test("recovers, signs, funds, and transfers with a browser wallet @hermetic", async ({
  page,
}) => {
  const result = await page.evaluate(async (driverUrl) => {
    const driver = (await import(driverUrl)) as BrowserDriver;
    return driver.runWalletJourney();
  }, PLAYWRIGHT_DRIVER_URL);

  expect(result.restoredAddress).toMatch(/^sparkl1/);
  expect(result.transferId).not.toHaveLength(0);
  expect(BigInt(result.receiverBalance)).toBeGreaterThanOrEqual(50_000n);
});

test("creates, mints, and fulfills a token invoice in the browser @hermetic", async ({
  page,
}) => {
  const result = await page.evaluate(async (driverUrl) => {
    const driver = (await import(driverUrl)) as BrowserDriver;
    return driver.runTokenLifecycle();
  }, PLAYWRIGHT_DRIVER_URL);

  expect(result.tokenIdentifier).not.toHaveLength(0);
  expect(result.transactionId).not.toHaveLength(0);
  expect(BigInt(result.receiverBalance)).toBeGreaterThanOrEqual(250n);

  const expectedHash = await sha256(TOKEN_WASM_PATH);
  expect(await instantiatedWasmHashes(page)).toContain(expectedHash);
  test.info().annotations.push({
    type: "binding-provenance",
    description: `token primitives ${expectedHash}`,
  });
});
