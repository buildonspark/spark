import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { expect, test, type Locator, type Page } from "@playwright/test";
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
const PLAYWRIGHT_DRIVER_URL = "/playwright/browser-driver.ts";
const RESULT_VISIBILITY_MS = 500;

type BrowserDriver = {
  startFrostBindingCallRecording(): string[];
  finishFrostBindingCallRecording(): string[];
};

type WalletDetails = {
  address: string;
  mnemonic: string;
};

function section(page: Page, heading: string): Locator {
  return page
    .getByRole("heading", { name: heading, exact: true })
    .locator("..");
}

function status(page: Page): Locator {
  return page.locator(".status");
}

async function sha256(filePath: string): Promise<string> {
  return createHash("sha256")
    .update(await readFile(filePath))
    .digest("hex");
}

async function pauseForRecordedResult(page: Page): Promise<void> {
  await page.waitForTimeout(RESULT_VISIBILITY_MS);
}

async function selectLocalTarget(page: Page): Promise<void> {
  const local = page.getByRole("button", { name: "LOCAL", exact: true });
  await expect(local).toBeVisible();
  await local.click();
  await expect(local).toHaveClass(/active/);
  await expect(
    page.getByRole("button", { name: "REGTEST", exact: true }),
  ).toHaveClass(/active/);
}

async function startBindingRecording(page: Page): Promise<string[]> {
  return page.evaluate(async (driverUrl) => {
    const driver = (await import(driverUrl)) as BrowserDriver;
    return driver.startFrostBindingCallRecording();
  }, PLAYWRIGHT_DRIVER_URL);
}

async function finishBindingRecording(page: Page): Promise<string[]> {
  return page.evaluate(async (driverUrl) => {
    const driver = (await import(driverUrl)) as BrowserDriver;
    return driver.finishFrostBindingCallRecording();
  }, PLAYWRIGHT_DRIVER_URL);
}

async function expectInstalledFrostWasm(page: Page): Promise<void> {
  expect(await instantiatedWasmHashes(page)).toContain(
    await sha256(FROST_WASM_PATH),
  );
}

async function generateWallet(page: Page): Promise<WalletDetails> {
  await page.getByRole("button", { name: "Generate New" }).click();
  await expect(status(page)).toHaveText("Wallet generated!", {
    timeout: 60_000,
  });

  const mnemonic = await page
    .getByPlaceholder("Enter 12 or 24 word mnemonic...")
    .inputValue();
  expect([12, 24]).toContain(mnemonic.trim().split(/\s+/).length);

  const address = (
    await section(page, "3. Wallet Info").locator(".code").innerText()
  ).trim();
  expect(address).toMatch(/^sparkl1/);
  await pauseForRecordedResult(page);
  return { address, mnemonic };
}

async function loadWallet(
  page: Page,
  mnemonic: string,
): Promise<WalletDetails> {
  await page.getByPlaceholder("Enter 12 or 24 word mnemonic...").fill(mnemonic);
  await page.getByRole("button", { name: "Load Wallet" }).click();
  await expect(status(page)).toHaveText("Wallet initialized!", {
    timeout: 60_000,
  });

  const address = (
    await section(page, "3. Wallet Info").locator(".code").innerText()
  ).trim();
  await pauseForRecordedResult(page);
  return { address, mnemonic };
}

async function copyCodeValue(
  page: Page,
  value: Locator,
  expectedClipboardValue?: string,
): Promise<string> {
  const displayedValue = (await value.innerText()).trim();
  await value.click();
  await expect(status(page)).toHaveText("Copied to clipboard!");
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    expectedClipboardValue ?? displayedValue,
  );
  return displayedValue;
}

async function bitcoinRpc<T>(
  page: Page,
  method: string,
  params: unknown[],
): Promise<T> {
  return page.evaluate(
    async ({ rpcMethod, rpcParams }) => {
      const response = await fetch("/bitcoin-rpc", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          jsonrpc: "1.0",
          id: "spark-vite-app-playwright",
          method: rpcMethod,
          params: rpcParams,
        }),
      });
      if (!response.ok) {
        throw new Error(`Bitcoin RPC returned HTTP ${response.status}`);
      }

      const payload = (await response.json()) as {
        result: T;
        error: { message: string } | null;
      };
      if (payload.error) {
        throw new Error(`Bitcoin RPC failed: ${payload.error.message}`);
      }
      return payload.result;
    },
    { rpcMethod: method, rpcParams: params },
  );
}

async function fundAddress(
  page: Page,
  address: string,
  amountSats: number,
): Promise<string> {
  const txid = await bitcoinRpc<string>(page, "sendtoaddress", [
    address,
    Number((amountSats / 100_000_000).toFixed(8)),
  ]);
  const miningAddress = await bitcoinRpc<string>(page, "getnewaddress", []);
  await bitcoinRpc(page, "generatetoaddress", [3, miningAddress]);
  return txid;
}

async function displayedBalance(page: Page): Promise<number> {
  const text = await section(page, "3. Wallet Info")
    .locator(".balance-amount")
    .innerText();
  return Number(text.replaceAll(",", ""));
}

async function refreshUntilBalance(
  page: Page,
  minimumBalance: number,
): Promise<void> {
  await expect
    .poll(
      async () => {
        await section(page, "3. Wallet Info")
          .getByRole("button", { name: "Refresh Balance" })
          .click();
        await expect(status(page)).toHaveText(/^Balance: \d+ sats$/, {
          timeout: 15_000,
        });
        return displayedBalance(page);
      },
      { timeout: 90_000, intervals: [1_000, 2_000, 3_000] },
    )
    .toBeGreaterThanOrEqual(minimumBalance);
}

test.beforeEach(async ({ context, page }) => {
  await installWasmRecorder(context);
  await loadApp(page);
});

test("selects networks and signs through the app @bindings", async ({
  page,
}) => {
  const prod = page.getByRole("button", { name: "PROD", exact: true });
  await prod.click();
  await expect(prod).toHaveClass(/active/);
  for (const network of ["MAINNET", "TESTNET", "REGTEST"]) {
    const option = page.getByRole("button", { name: network, exact: true });
    await option.click();
    await expect(option).toHaveClass(/active/);
  }
  await pauseForRecordedResult(page);

  const dev = page.getByRole("button", { name: "DEV", exact: true });
  if ((await dev.count()) > 0) {
    await dev.click();
    await expect(dev).toHaveClass(/active/);
    for (const network of ["MAINNET", "REGTEST"]) {
      const option = page.getByRole("button", { name: network, exact: true });
      if ((await option.count()) > 0) {
        await option.click();
        await expect(option).toHaveClass(/active/);
      }
    }
    await pauseForRecordedResult(page);
  }

  const local = page.getByRole("button", { name: "LOCAL", exact: true });
  if ((await local.count()) > 0) {
    await selectLocalTarget(page);
    await pauseForRecordedResult(page);
  }

  const availableMethods = await startBindingRecording(page);
  expect(availableMethods).toContain("createDummyTx");

  await page.getByRole("button", { name: "Test WASM Signing" }).click();
  await expect(status(page)).toHaveText("WASM works!");
  await expect(section(page, "1. Test WASM").locator(".code")).toHaveText(
    /^txid: [0-9a-f]{64}$/,
  );
  await pauseForRecordedResult(page);
  const wasmResult = section(page, "1. Test WASM").locator(".code");
  const txid = (await wasmResult.innerText()).replace(/^txid: /, "");
  await copyCodeValue(page, wasmResult, txid);

  await page.getByRole("button", { name: "Load Wallet" }).click();
  await expect(status(page)).toHaveText("Enter a mnemonic");

  expect(await finishBindingRecording(page)).toContain("createDummyTx");
  await expectInstalledFrostWasm(page);
});

test("generates, restores, and inspects a wallet through the app @hermetic", async ({
  page,
}) => {
  await selectLocalTarget(page);
  const generated = await generateWallet(page);
  await copyCodeValue(page, section(page, "3. Wallet Info").locator(".code"));

  await loadApp(page);
  await selectLocalTarget(page);
  const restored = await loadWallet(page, generated.mnemonic);
  expect(restored.address).toBe(generated.address);

  await section(page, "3. Wallet Info")
    .getByRole("button", { name: "Refresh Balance" })
    .click();
  await expect(status(page)).toHaveText(/^Balance: \d+ sats$/, {
    timeout: 30_000,
  });
  await expect(
    section(page, "3. Wallet Info").locator(".balance-amount"),
  ).toBeVisible();
});

test("creates, claims, funds, and transfers through the app @hermetic", async ({
  context,
  page,
}) => {
  test.setTimeout(360_000);
  await selectLocalTarget(page);
  await startBindingRecording(page);
  await generateWallet(page);

  const deposit = section(page, "4. Deposit");
  await deposit.getByRole("button", { name: "Get Deposit Address" }).click();
  await expect(status(page)).toHaveText("Deposit address ready!", {
    timeout: 30_000,
  });
  const depositAddress = (await deposit.locator(".code").innerText()).trim();
  expect(depositAddress).toMatch(/^bcrt1/);

  const manualDepositSats = 25_000;
  const txid = await fundAddress(page, depositAddress, manualDepositSats);
  await deposit.getByPlaceholder("Deposit transaction ID").fill(txid);
  await deposit.getByRole("button", { name: "Claim Deposit" }).click();
  await expect(status(page)).toHaveText(
    new RegExp(`^Claimed ${manualDepositSats} sats from deposit ${txid}$`),
    { timeout: 120_000 },
  );
  expect(await displayedBalance(page)).toBeGreaterThanOrEqual(
    manualDepositSats,
  );
  await status(page).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(page);

  const localFundingSats = 30_000;
  await deposit.getByPlaceholder("Amount (sats)").fill(`${localFundingSats}`);
  await deposit.getByRole("button", { name: "Fund Locally" }).click();
  await expect(status(page)).toHaveText(
    new RegExp(
      `^Funded and claimed ${localFundingSats} sats \\([0-9a-f]{64}\\)$`,
    ),
    { timeout: 120_000 },
  );
  const fundedBalance = await displayedBalance(page);
  expect(fundedBalance).toBeGreaterThanOrEqual(
    manualDepositSats + localFundingSats,
  );
  await status(page).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(page);

  await section(page, "3. Wallet Info")
    .getByRole("button", { name: "Refresh Balance" })
    .click();
  await expect(status(page)).toHaveText(/^Balance: \d+ sats$/, {
    timeout: 30_000,
  });

  const receiverPage = await context.newPage();
  await loadApp(receiverPage);
  await selectLocalTarget(receiverPage);
  await startBindingRecording(receiverPage);
  const receiver = await generateWallet(receiverPage);

  const transferSats = fundedBalance;
  const send = section(page, "6. Send");
  await send.getByRole("button", { name: "Spark", exact: true }).click();
  await send.getByPlaceholder("Recipient Spark address").fill(receiver.address);
  await send.getByPlaceholder("Amount (sats)").fill(`${transferSats}`);
  await send.getByRole("button", { name: "Send", exact: true }).click();
  await expect(status(page)).toHaveText(/^Sent! ID: .+$/, {
    timeout: 120_000,
  });
  await status(page).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(page);

  await refreshUntilBalance(receiverPage, transferSats);
  await pauseForRecordedResult(receiverPage);

  expect(await finishBindingRecording(page)).toEqual(
    expect.arrayContaining(["encryptEcies", "signFrost"]),
  );
  await finishBindingRecording(receiverPage);
  await expectInstalledFrostWasm(page);
  await receiverPage.close();
});

test("creates and pays a Lightning invoice through the app @ssp", async ({
  context,
  page,
}) => {
  test.skip(
    process.env["HERMETIC_TEST"] !== "true",
    "requires the SSP hermetic environment",
  );
  test.setTimeout(360_000);

  await selectLocalTarget(page);
  await startBindingRecording(page);
  await generateWallet(page);

  const payerFundingSats = 50_000;
  const deposit = section(page, "4. Deposit");
  await deposit.getByPlaceholder("Amount (sats)").fill(`${payerFundingSats}`);
  await deposit.getByRole("button", { name: "Fund Locally" }).click();
  await expect(status(page)).toHaveText(
    new RegExp(
      `^Funded and claimed ${payerFundingSats} sats \\([0-9a-f]{64}\\)$`,
    ),
    { timeout: 120_000 },
  );

  const receiverPage = await context.newPage();
  await loadApp(receiverPage);
  await selectLocalTarget(receiverPage);
  await startBindingRecording(receiverPage);
  await generateWallet(receiverPage);

  const invoiceSats = 10_000;
  const receive = section(receiverPage, "5. Receive");
  await receive.getByPlaceholder("Amount (sats)").fill(`${invoiceSats}`);
  await receive.getByRole("button", { name: "Create Invoice" }).click();
  await expect(status(receiverPage)).toHaveText("Invoice created!", {
    timeout: 120_000,
  });
  await status(receiverPage).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(receiverPage);
  const invoice = await copyCodeValue(receiverPage, receive.locator(".code"));
  expect(invoice).toMatch(/^ln/);
  await pauseForRecordedResult(receiverPage);

  const send = section(page, "6. Send");
  await send.getByRole("button", { name: "Lightning", exact: true }).click();
  await send.getByPlaceholder("Lightning invoice (lnbc...)").fill(invoice);
  await send.getByPlaceholder("Max fee (sats)").fill("100");
  await send.getByRole("button", { name: "Pay Invoice" }).click();
  await expect(status(page)).toHaveText(/^Paid! ID: .+$/, {
    timeout: 120_000,
  });
  await status(page).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(page);

  await refreshUntilBalance(receiverPage, invoiceSats);
  await pauseForRecordedResult(receiverPage);

  expect(await finishBindingRecording(receiverPage)).toEqual(
    expect.arrayContaining(["encryptEcies", "splitSecretWithProofs"]),
  );
  expect(await finishBindingRecording(page)).toEqual(
    expect.arrayContaining(["aggregateFrost", "signFrost"]),
  );
  await expectInstalledFrostWasm(page);
  await expectInstalledFrostWasm(receiverPage);
  await receiverPage.close();
});
