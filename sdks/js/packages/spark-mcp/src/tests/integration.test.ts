/**
 * End-to-end integration test for the deposit round-trip scenario.
 * Runs against a live Spark environment (minikube or run-everything.sh).
 *
 * Run with:
 *   BITCOIN_NETWORK=LOCAL SPARK_LOCAL_INGRESS_HOST=192.168.49.2 \
 *     yarn test-cmd src/tests/integration.test.ts
 *
 * SPARK_MNEMONIC is optional. When omitted, set SPARK_MNEMONIC to a wallet
 * mnemonic before running, or the tools will fail with "No wallet specified".
 * Use spark_create_wallet to generate a fresh mnemonic for testing.
 */

import { describe, it, expect } from "@jest/globals";
import {
  handleGetDepositAddress,
  handleClaimDeposit,
} from "../tools/deposits.js";
import { handleFundAddress } from "../tools/funding.js";
import { handleGetBalance } from "../tools/wallet.js";

function extractText(result: {
  content: Array<{ type: string; text: string }>;
  isError?: boolean;
}): string {
  if (result.isError) throw new Error(`Tool error: ${result.content[0]?.text}`);
  return result.content[0]?.text ?? "";
}

const isLive = !!process.env["BITCOIN_NETWORK"];
(isLive ? describe : describe.skip)("deposit round-trip", () => {
  let depositAddress: string;
  let txid: string;
  let balanceBeforeSats: number;

  it("gets a deposit address", async () => {
    const result = await handleGetDepositAddress();
    const text = extractText(result);
    const match = text.match(/Deposit address: (\S+)/);
    expect(match).not.toBeNull();
    depositAddress = match![1]!;
    console.log(`  deposit address: ${depositAddress}`);
  }, 15_000);

  it("records balance before deposit", async () => {
    const text = extractText(await handleGetBalance());
    const match = text.match(/([\d,]+) sats/);
    expect(match).not.toBeNull();
    balanceBeforeSats = parseInt(match![1]!.replace(/,/g, ""), 10);
    console.log(`  balance before: ${text}`);
  }, 15_000);

  it("funds the deposit address with 50,000 sats", async () => {
    expect(depositAddress).toBeDefined();
    const result = await handleFundAddress(depositAddress, 50_000, 1);
    const text = extractText(result);
    const match = text.match(/Transaction ID: (\S+)/);
    expect(match).not.toBeNull();
    txid = match![1]!;
    console.log(`  txid: ${txid}`);
  }, 30_000);

  it("claims the deposit", async () => {
    expect(txid).toBeDefined();
    const result = await handleClaimDeposit(txid);
    const text = extractText(result);
    console.log(`  claim result: ${text}`);
    expect(text).toContain("claimed");
  }, 60_000);

  it("balance increased by 50,000 sats", async () => {
    const deadline = Date.now() + 90_000;
    let balanceAfterSats = 0;
    while (Date.now() < deadline) {
      const text = extractText(await handleGetBalance());
      const match = text.match(/([\d,]+) sats/);
      if (match) {
        balanceAfterSats = parseInt(match[1]!.replace(/,/g, ""), 10);
        if (balanceAfterSats >= balanceBeforeSats + 50_000) {
          console.log(`  balance after: ${text}`);
          break;
        }
      }
      await new Promise((r) => setTimeout(r, 2_000));
    }
    expect(balanceAfterSats).toBe(balanceBeforeSats + 50_000);
  }, 120_000);
});
