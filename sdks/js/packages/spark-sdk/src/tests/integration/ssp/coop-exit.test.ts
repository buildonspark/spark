import { describe, expect, it } from "@jest/globals";
import { SparkValidationError } from "../../../index-shared.js";
import { ExitSpeed } from "../../../types/index.js";
import { getNewAddress } from "../../utils/regtest-test-faucet.js";
import {
  initTestingWallet,
  SparkWalletTesting,
} from "../../utils/spark-testing-wallet.js";
import { retryUntilSuccess } from "../../utils/utils.js";

export const DEPOSIT_AMOUNT = 30_000n;

describe("SSP coop exit integration", () => {
  let userWallet!: SparkWalletTesting;
  let withdrawalAddress: string;
  let quoteAmount: number;

  beforeEach(async () => {
    const { wallet, signedTx, vout } = await initTestingWallet(
      DEPOSIT_AMOUNT,
      "LOCAL",
    );

    const transactionId = await retryUntilSuccess(async () => {
      if (!signedTx) throw new Error("Tx not mined yet");
      return signedTx.id;
    });

    userWallet = wallet;

    console.log("Fetching claim quote for static deposit...");
    const quote = await retryUntilSuccess(async () => {
      const q = await userWallet.getClaimStaticDepositQuote(
        transactionId,
        vout!,
      );
      if (!q) throw new Error("Quote not available yet");
      return q;
    });

    quoteAmount = quote.creditAmountSats;
    const sspSignature = quote.signature;

    console.log("Attempting to claim static deposit...");
    await userWallet.claimStaticDeposit({
      transactionId,
      creditAmountSats: quoteAmount,
      sspSignature,
      outputIndex: vout!,
    });

    await retryUntilSuccess(async () => {
      const { balance } = await userWallet.getBalance();
      if (balance === BigInt(quoteAmount)) return balance;
      throw new Error("Balance incorrect value");
    });

    withdrawalAddress = await getNewAddress();
  }, 600000);
  it("should estimate coop exit fee", async () => {
    const feeEstimate = await userWallet.getWithdrawalFeeQuote({
      amountSats: Number(quoteAmount),
      withdrawalAddress,
    });

    expect(feeEstimate).toBeDefined();
    expect(feeEstimate?.l1BroadcastFeeFast).toBeDefined();
    expect(feeEstimate?.l1BroadcastFeeFast.originalValue).toBeGreaterThan(0);
    expect(feeEstimate?.userFeeFast).toBeDefined();
    expect(feeEstimate?.userFeeFast.originalValue).toBeGreaterThan(0);

    expect(feeEstimate?.l1BroadcastFeeMedium).toBeDefined();
    expect(feeEstimate?.l1BroadcastFeeMedium.originalValue).toBeGreaterThan(0);
    expect(feeEstimate?.userFeeMedium).toBeDefined();
    expect(feeEstimate?.userFeeMedium.originalValue).toBeGreaterThan(0);

    expect(feeEstimate?.l1BroadcastFeeSlow).toBeDefined();
    expect(feeEstimate?.l1BroadcastFeeSlow.originalValue).toBeGreaterThan(0);
    expect(feeEstimate?.userFeeSlow).toBeDefined();
    expect(feeEstimate?.userFeeSlow.originalValue).toBeGreaterThan(0);
  }, 600000);

  it("should complete coop exit without deducting fees from withdrawal amount", async () => {
    const { balance } = await userWallet.getBalance();
    expect(balance).toBe(BigInt(quoteAmount));

    const feeQuote = await userWallet.getWithdrawalFeeQuote({
      amountSats: 5000,
      withdrawalAddress,
    });

    expect(feeQuote).toBeDefined();

    const coopExit = await userWallet.withdraw({
      amountSats: 5000,
      onchainAddress: withdrawalAddress,
      feeQuote: feeQuote!,
      exitSpeed: ExitSpeed.FAST,
      deductFeeFromWithdrawalAmount: false,
    });

    const fee =
      (coopExit?.l1BroadcastFee?.originalValue ?? 0) +
      (coopExit?.fee?.originalValue ?? 0);

    expect(fee).toBeGreaterThan(0);

    const { balance: balanceAfter } = await userWallet.getBalance();

    expect(balanceAfter).toBe(balance - 5000n - BigInt(fee));
    expect(coopExit).toBeDefined();
    expect(coopExit?.coopExitTxid).toBeDefined();
  }, 600000);

  it("CoopExit with spent leaves", async () => {
    const { balance } = await userWallet.getBalance();

    const feeQuote = await userWallet.getWithdrawalFeeQuote({
      amountSats: Number(balance),
      withdrawalAddress,
    });

    expect(feeQuote).toBeDefined();

    const coopExit = await userWallet.withdraw({
      amountSats: Number(balance),
      onchainAddress: withdrawalAddress,
      feeQuote: feeQuote!,
      exitSpeed: ExitSpeed.FAST,
      deductFeeFromWithdrawalAmount: true,
    });

    expect(coopExit).toBeDefined();
    expect(coopExit?.coopExitTxid).toBeDefined();

    const sparkAddress = await userWallet.getSparkAddress();
    await expect(
      userWallet.transfer({
        amountSats: Number(balance),
        receiverSparkAddress: sparkAddress,
      }),
    ).rejects.toMatchObject({
      name: SparkValidationError.name,
      message: expect.stringContaining("No owned leaves found"),
      context: expect.objectContaining({
        field: "leaves",
      }),
    });
  }, 600000);
});
