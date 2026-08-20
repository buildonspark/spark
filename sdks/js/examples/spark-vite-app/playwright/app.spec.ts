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

async function initializeLocalWallet(page: Page): Promise<WalletDetails> {
  await selectLocalTarget(page);
  return generateWallet(page);
}

async function runWasmSigning(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Test WASM Signing" }).click();
  await expect(status(page)).toHaveText("WASM works!");

  const result = section(page, "1. Test WASM").locator(".code");
  await expect(result).toHaveText(/^txid: [0-9a-f]{64}$/);
  await pauseForRecordedResult(page);
  return result;
}

async function fundLocallyThroughApp(
  page: Page,
  amountSats: number,
): Promise<number> {
  const deposit = section(page, "4. Deposit");
  await deposit.getByPlaceholder("Amount (sats)").fill(`${amountSats}`);
  await deposit.getByRole("button", { name: "Fund Locally" }).click();
  await expect(status(page)).toHaveText(
    new RegExp(`^Funded and claimed ${amountSats} sats \\([0-9a-f]{64}\\)$`),
    { timeout: 120_000 },
  );
  return displayedBalance(page);
}

async function createInvoiceThroughApp(
  page: Page,
  amountSats: number,
): Promise<string> {
  const receive = section(page, "5. Receive");
  await receive.getByPlaceholder("Amount (sats)").fill(`${amountSats}`);
  await receive.getByRole("button", { name: "Create Invoice" }).click();
  await expect(status(page)).toHaveText("Invoice created!", {
    timeout: 120_000,
  });

  const invoice = (await receive.locator(".code").innerText()).trim();
  expect(invoice).toMatch(/^ln/);
  return invoice;
}

async function expectFrostRuntimeCalls(
  page: Page,
  expectedMethods: string[],
): Promise<void> {
  expect(await finishBindingRecording(page)).toEqual(
    expect.arrayContaining(expectedMethods),
  );
  await expectInstalledFrostWasm(page);
}

test.beforeEach(async ({ context, page }) => {
  await installWasmRecorder(context);
  await loadApp(page);
});

test("selects PROD and every public network through the app", async ({
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
});

test("selects every configured DEV network through the app", async ({
  page,
}) => {
  const dev = page.getByRole("button", { name: "DEV", exact: true });
  test.skip((await dev.count()) === 0, "requires private DEV configs");

  await dev.click();
  await expect(dev).toHaveClass(/active/);
  for (const network of ["MAINNET", "REGTEST"]) {
    const option = page.getByRole("button", { name: network, exact: true });
    await option.click();
    await expect(option).toHaveClass(/active/);
  }
  await expect(
    page.getByRole("button", { name: "TESTNET", exact: true }),
  ).toHaveCount(0);
  await pauseForRecordedResult(page);
});

test("selects LOCAL and locks the app to REGTEST", async ({ page }) => {
  const local = page.getByRole("button", { name: "LOCAL", exact: true });
  test.skip((await local.count()) === 0, "requires local Spark config");

  await page.getByRole("button", { name: "PROD", exact: true }).click();
  await page.getByRole("button", { name: "MAINNET", exact: true }).click();
  await selectLocalTarget(page);
  await expect(
    page.getByRole("button", { name: "MAINNET", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "TESTNET", exact: true }),
  ).toBeDisabled();
  await pauseForRecordedResult(page);
});

test("hides wallet operations until initialization @hermetic", async ({
  page,
}) => {
  for (const heading of [
    "3. Wallet Info",
    "4. Deposit",
    "5. Receive",
    "6. Send",
  ]) {
    await expect(page.getByRole("heading", { name: heading })).toHaveCount(0);
  }

  await initializeLocalWallet(page);

  for (const heading of [
    "3. Wallet Info",
    "4. Deposit",
    "5. Receive",
    "6. Send",
  ]) {
    await expect(page.getByRole("heading", { name: heading })).toBeVisible();
  }
});

test("signs through the installed browser WASM @bindings", async ({ page }) => {
  const availableMethods = await startBindingRecording(page);
  expect(availableMethods).toContain("createDummyTx");

  await runWasmSigning(page);
  await expectFrostRuntimeCalls(page, ["createDummyTx"]);
});

test("copies the WASM transaction ID", async ({ page }) => {
  const wasmResult = await runWasmSigning(page);
  const txid = (await wasmResult.innerText()).replace(/^txid: /, "");
  await copyCodeValue(page, wasmResult, txid);
});

test("reports clipboard permission failures", async ({ page }) => {
  const wasmResult = await runWasmSigning(page);
  await page.evaluate(() => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: () => Promise.reject(new Error("clipboard denied")),
      },
    });
  });

  await wasmResult.click();
  await expect(status(page)).toHaveText("Failed to copy");
});

test("rejects loading an empty mnemonic", async ({ page }) => {
  await page.getByRole("button", { name: "Load Wallet" }).click();
  await expect(status(page)).toHaveText("Enter a mnemonic");
});

test("generates a new wallet through the app @hermetic", async ({ page }) => {
  await selectLocalTarget(page);
  const generated = await generateWallet(page);

  expect(generated.address).toMatch(/^sparkl1/);
  await expect(
    page.getByPlaceholder("Enter 12 or 24 word mnemonic..."),
  ).toHaveValue(generated.mnemonic);
});

test("restores an existing wallet from its mnemonic @hermetic", async ({
  page,
}) => {
  await selectLocalTarget(page);
  const generated = await generateWallet(page);

  await loadApp(page);
  await selectLocalTarget(page);
  const restored = await loadWallet(page, generated.mnemonic);
  expect(restored.address).toBe(generated.address);
});

test("copies the initialized wallet Spark address @hermetic", async ({
  page,
}) => {
  await initializeLocalWallet(page);
  await copyCodeValue(page, section(page, "3. Wallet Info").locator(".code"));
});

test("refreshes and displays the wallet balance @hermetic", async ({
  page,
}) => {
  await initializeLocalWallet(page);

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

test("clears stale wallet state when generating another wallet @hermetic", async ({
  page,
}) => {
  const firstWallet = await initializeLocalWallet(page);
  const walletInfo = section(page, "3. Wallet Info");
  const deposit = section(page, "4. Deposit");

  await walletInfo.getByRole("button", { name: "Refresh Balance" }).click();
  await expect(walletInfo.locator(".balance-display")).toBeVisible();

  await deposit.getByRole("button", { name: "Get Deposit Address" }).click();
  await expect(status(page)).toHaveText("Deposit address ready!", {
    timeout: 30_000,
  });
  await deposit.getByPlaceholder("Deposit transaction ID").fill("stale-txid");

  const secondWallet = await generateWallet(page);
  expect(secondWallet.address).not.toBe(firstWallet.address);
  await expect(walletInfo.locator(".balance-display")).toHaveCount(0);
  await expect(deposit.locator(".code")).toHaveCount(0);
  await expect(deposit.getByPlaceholder("Deposit transaction ID")).toHaveValue(
    "",
  );
});

test("creates and copies a single-use deposit address @hermetic", async ({
  page,
}) => {
  await initializeLocalWallet(page);
  const deposit = section(page, "4. Deposit");
  await deposit.getByPlaceholder("Deposit transaction ID").fill("stale-txid");
  await deposit.getByRole("button", { name: "Get Deposit Address" }).click();
  await expect(status(page)).toHaveText("Deposit address ready!", {
    timeout: 30_000,
  });

  const depositAddress = (await deposit.locator(".code").innerText()).trim();
  expect(depositAddress).toMatch(/^bcrt1/);
  await expect(deposit.getByPlaceholder("Deposit transaction ID")).toHaveValue(
    "",
  );
  await copyCodeValue(page, deposit.locator(".code"));
});

test("rejects an empty deposit transaction ID @hermetic", async ({ page }) => {
  await initializeLocalWallet(page);
  await section(page, "4. Deposit")
    .getByRole("button", { name: "Claim Deposit" })
    .click();
  await expect(status(page)).toHaveText("Enter a deposit transaction ID");
});

test("claims a manually funded deposit and updates balance @hermetic", async ({
  page,
}) => {
  test.setTimeout(180_000);
  await initializeLocalWallet(page);

  const deposit = section(page, "4. Deposit");
  await deposit.getByRole("button", { name: "Get Deposit Address" }).click();
  await expect(status(page)).toHaveText("Deposit address ready!", {
    timeout: 30_000,
  });
  const depositAddress = (await deposit.locator(".code").innerText()).trim();

  const manualDepositSats = 25_000;
  const txid = await fundAddress(page, depositAddress, manualDepositSats);
  await deposit.getByPlaceholder("Deposit transaction ID").fill(txid);
  await startBindingRecording(page);
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
  await expectFrostRuntimeCalls(page, ["signFrost"]);
});

test("rejects invalid local funding amounts @hermetic", async ({ page }) => {
  await initializeLocalWallet(page);
  const deposit = section(page, "4. Deposit");
  const amount = deposit.getByPlaceholder("Amount (sats)");

  for (const invalidAmount of ["", "0", "-1"]) {
    await amount.fill(invalidAmount);
    await deposit.getByRole("button", { name: "Fund Locally" }).click();
    await expect(status(page)).toHaveText("Enter a valid deposit amount");
  }
});

test("funds locally and disables deposit controls while pending @hermetic", async ({
  page,
}) => {
  test.setTimeout(180_000);
  await initializeLocalWallet(page);

  const deposit = section(page, "4. Deposit");
  const amount = deposit.getByPlaceholder("Amount (sats)");
  const txid = deposit.getByPlaceholder("Deposit transaction ID");
  const getAddress = deposit.getByRole("button", {
    name: "Get Deposit Address",
  });
  const fund = deposit.getByRole("button", { name: "Fund Locally" });
  const claim = deposit.getByRole("button", { name: "Claim Deposit" });

  const localFundingSats = 30_000;
  await amount.fill(`${localFundingSats}`);
  await startBindingRecording(page);
  await fund.click();
  await expect(getAddress).toBeDisabled();
  await expect(amount).toBeDisabled();
  await expect(fund).toBeDisabled();
  await expect(txid).toBeDisabled();
  await expect(claim).toBeDisabled();
  await expect(status(page)).toHaveText(
    new RegExp(
      `^Funded and claimed ${localFundingSats} sats \\([0-9a-f]{64}\\)$`,
    ),
    { timeout: 120_000 },
  );
  expect(await displayedBalance(page)).toBeGreaterThanOrEqual(localFundingSats);
  await expect(deposit.locator(".code")).toHaveText(/^bcrt1/);
  await expect(txid).toHaveValue(/^[0-9a-f]{64}$/);
  await expect(getAddress).toBeEnabled();
  await expect(amount).toBeEnabled();
  await expect(fund).toBeEnabled();
  await expect(txid).toBeEnabled();
  await expect(claim).toBeEnabled();
  await status(page).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(page);
  await expectFrostRuntimeCalls(page, ["signFrost"]);
});

test("reports local funding failures and restores deposit controls @hermetic", async ({
  page,
}) => {
  await initializeLocalWallet(page);
  await page.route("**/bitcoin-rpc", async (route) => {
    await route.fulfill({ status: 500, body: "Bitcoin RPC unavailable" });
  });

  const deposit = section(page, "4. Deposit");
  const amount = deposit.getByPlaceholder("Amount (sats)");
  const txid = deposit.getByPlaceholder("Deposit transaction ID");
  const fund = deposit.getByRole("button", { name: "Fund Locally" });
  const claim = deposit.getByRole("button", { name: "Claim Deposit" });

  await amount.fill("10000");
  await fund.click();
  await expect(amount).toBeDisabled();
  await expect(txid).toBeDisabled();
  await expect(claim).toBeDisabled();
  await expect(status(page)).toHaveText("Error: Bitcoin RPC HTTP error: 500", {
    timeout: 30_000,
  });
  await expect(amount).toBeEnabled();
  await expect(txid).toBeEnabled();
  await expect(fund).toBeEnabled();
  await expect(claim).toBeEnabled();
});

test("rejects invalid Lightning invoice amounts @hermetic", async ({
  page,
}) => {
  await initializeLocalWallet(page);
  const receive = section(page, "5. Receive");
  const amount = receive.getByPlaceholder("Amount (sats)");

  for (const invalidAmount of ["", "0", "-1", "1.5", "1e3"]) {
    await amount.fill(invalidAmount);
    await receive.getByRole("button", { name: "Create Invoice" }).click();
    await expect(status(page)).toHaveText("Enter a valid invoice amount");
  }
});

test("switches between the Spark and Lightning send forms @hermetic", async ({
  page,
}) => {
  await initializeLocalWallet(page);
  const send = section(page, "6. Send");
  const spark = send.getByRole("button", { name: "Spark", exact: true });
  const lightning = send.getByRole("button", {
    name: "Lightning",
    exact: true,
  });

  await expect(spark).toHaveClass(/active/);
  await expect(send.getByPlaceholder("Recipient Spark address")).toBeVisible();
  await expect(send.getByPlaceholder("Amount (sats)")).toBeVisible();
  await expect(
    send.getByRole("button", { name: "Send", exact: true }),
  ).toBeVisible();

  await lightning.click();
  await expect(lightning).toHaveClass(/active/);
  await expect(
    send.getByPlaceholder("Lightning invoice (lnbc...)"),
  ).toBeVisible();
  await expect(send.getByPlaceholder("Max fee (sats)")).toHaveValue("100");
  await expect(send.getByRole("button", { name: "Pay Invoice" })).toBeVisible();

  await spark.click();
  await expect(spark).toHaveClass(/active/);
  await expect(send.getByPlaceholder("Recipient Spark address")).toBeVisible();
});

test("rejects a missing Spark recipient or Lightning invoice @hermetic", async ({
  page,
}) => {
  await initializeLocalWallet(page);
  const send = section(page, "6. Send");

  await send.getByRole("button", { name: "Send", exact: true }).click();
  await expect(status(page)).toHaveText("Enter recipient address or invoice");

  await send.getByRole("button", { name: "Lightning", exact: true }).click();
  await send.getByRole("button", { name: "Pay Invoice" }).click();
  await expect(status(page)).toHaveText("Enter recipient address or invoice");
});

test("rejects invalid Spark transfer amounts @hermetic", async ({ page }) => {
  await initializeLocalWallet(page);
  const send = section(page, "6. Send");
  await send
    .getByPlaceholder("Recipient Spark address")
    .fill("sparkl1invalidforthevalidationpath");

  const amount = send.getByPlaceholder("Amount (sats)");
  for (const invalidAmount of ["", "0", "-1"]) {
    await amount.fill(invalidAmount);
    await send.getByRole("button", { name: "Send", exact: true }).click();
    await expect(status(page)).toHaveText("Enter valid amount");
  }
});

test("sends Spark and verifies the recipient balance @hermetic", async ({
  context,
  page,
}) => {
  test.setTimeout(300_000);
  await initializeLocalWallet(page);
  const fundedBalance = await fundLocallyThroughApp(page, 50_000);

  const receiverPage = await context.newPage();
  await loadApp(receiverPage);
  const receiver = await initializeLocalWallet(receiverPage);

  const transferSats = fundedBalance;
  const send = section(page, "6. Send");
  await send.getByRole("button", { name: "Spark", exact: true }).click();
  await send.getByPlaceholder("Recipient Spark address").fill(receiver.address);
  await send.getByPlaceholder("Amount (sats)").fill(`${transferSats}`);
  await startBindingRecording(page);
  await send.getByRole("button", { name: "Send", exact: true }).click();
  await expect(status(page)).toHaveText(/^Sent! ID: .+$/, {
    timeout: 120_000,
  });
  await status(page).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(page);

  await refreshUntilBalance(receiverPage, transferSats);
  await pauseForRecordedResult(receiverPage);

  await expectFrostRuntimeCalls(page, ["encryptEcies", "signFrost"]);
  await receiverPage.close();
});

test("creates and copies a Lightning invoice through the app @ssp", async ({
  page,
}) => {
  test.skip(
    process.env["HERMETIC_TEST"] !== "true",
    "requires the SSP hermetic environment",
  );
  test.setTimeout(240_000);

  await initializeLocalWallet(page);
  await startBindingRecording(page);
  const invoice = await createInvoiceThroughApp(page, 10_000);
  await status(page).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(page);
  await copyCodeValue(
    page,
    section(page, "5. Receive").locator(".code"),
    invoice,
  );
  await pauseForRecordedResult(page);
  await expectFrostRuntimeCalls(page, [
    "encryptEcies",
    "splitSecretWithProofs",
  ]);
});

test("pays a Lightning invoice and verifies the recipient balance @ssp", async ({
  context,
  page,
}) => {
  test.skip(
    process.env["HERMETIC_TEST"] !== "true",
    "requires the SSP hermetic environment",
  );
  test.setTimeout(360_000);

  await initializeLocalWallet(page);
  const payerFundingSats = 50_000;
  await fundLocallyThroughApp(page, payerFundingSats);

  const receiverPage = await context.newPage();
  await loadApp(receiverPage);
  await initializeLocalWallet(receiverPage);

  const invoiceSats = 10_000;
  const invoice = await createInvoiceThroughApp(receiverPage, invoiceSats);
  await status(receiverPage).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(receiverPage);

  const send = section(page, "6. Send");
  await send.getByRole("button", { name: "Lightning", exact: true }).click();
  await send.getByPlaceholder("Lightning invoice (lnbc...)").fill(invoice);
  await send.getByPlaceholder("Max fee (sats)").fill("200");
  await startBindingRecording(page);
  await send.getByRole("button", { name: "Pay Invoice" }).click();
  await expect(status(page)).toHaveText(/^Paid! ID: .+$/, {
    timeout: 120_000,
  });
  await status(page).scrollIntoViewIfNeeded();
  await pauseForRecordedResult(page);

  await refreshUntilBalance(receiverPage, invoiceSats);
  await pauseForRecordedResult(receiverPage);

  await expectFrostRuntimeCalls(page, ["aggregateFrost", "signFrost"]);
  await receiverPage.close();
});
