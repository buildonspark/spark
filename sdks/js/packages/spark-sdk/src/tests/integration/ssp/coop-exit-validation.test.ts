import { expect } from "@jest/globals";
import { SparkValidationError } from "../../../errors/types.js";
import { ExitSpeed } from "../../../types/index.js";
import { getNewAddress } from "../../utils/regtest-test-faucet.js";
import {
  initTestingWallet,
  SparkWalletTesting,
} from "../../utils/spark-testing-wallet.js";
import { retryUntilSuccess } from "../../utils/utils.js";

const DEPOSIT_AMOUNT = 50_000n;

describe("SSP coop exit basic validation", () => {
  let userWallet!: SparkWalletTesting;
  let withdrawalAddress: string;
  let quoteAmount: number;

  beforeAll(async () => {
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

  it("should fail when amountSats is zero", async () => {
    const { balance } = await userWallet.getBalance();
    expect(balance).toBe(BigInt(quoteAmount));

    await expect(
      userWallet.getWithdrawalFeeQuote({
        amountSats: 0,
        withdrawalAddress,
      }),
    ).rejects.toThrow("Target amount must be positive");
  }, 600000);

  it("should fail when amountSats is negative", async () => {
    await expect(
      userWallet.getWithdrawalFeeQuote({
        amountSats: -1,
        withdrawalAddress,
      }),
    ).rejects.toThrow("Target amount must be positive");
  }, 600000);

  it("should fail when amountSats exceeds Number.MAX_SAFE_INTEGER", async () => {
    await expect(
      userWallet.getWithdrawalFeeQuote({
        amountSats: Number.MAX_SAFE_INTEGER + 1,
        withdrawalAddress,
      }),
    ).rejects.toThrow("Sats amount must be less than 2^53");
  }, 600000);

  it("should fail when amountSats exceeds available balance", async () => {
    const { balance } = await userWallet.getBalance();
    expect(balance).toBe(BigInt(quoteAmount));

    await expect(
      userWallet.getWithdrawalFeeQuote({
        amountSats: Number(balance) + 1,
        withdrawalAddress,
      }),
    ).rejects.toThrow("Total target amount exceeds available balance");
  }, 600000);

  it("should fail when withdrawalAddress is invalid", async () => {
    await expect(
      userWallet.getWithdrawalFeeQuote({
        amountSats: 1000,
        withdrawalAddress: "invalid address",
      }),
    ).rejects.toThrow("Invalid address provided");
  }, 600000);

  it("should succeed when valid params are provided", async () => {
    const feeQuote = await userWallet.getWithdrawalFeeQuote({
      amountSats: 5000,
      withdrawalAddress,
    });

    expect(feeQuote).toBeDefined();
  }, 600000);

  it("should fail when withdrawalAddress is missing", async () => {
    await expect(
      userWallet.getWithdrawalFeeQuote({
        amountSats: 1000,
        withdrawalAddress: "" as unknown as string,
      }),
    ).rejects.toThrow("Invalid address provided");
  }, 600000);

  it("should fail when amountSats is not a number", async () => {
    await expect(
      userWallet.getWithdrawalFeeQuote({
        amountSats: "1000" as unknown as number,
        withdrawalAddress,
      }),
    ).rejects.toThrow("Sats amount must be less than 2^53");
  }, 600000);

  it("should fail if deductFeeFromWithdrawalAmount is true and amount is too small", async () => {
    const feeQuote = await userWallet.getWithdrawalFeeQuote({
      amountSats: 330,
      withdrawalAddress,
    });

    await expect(
      userWallet.withdraw({
        amountSats: 330, // Fails if amount is less than the fee.
        onchainAddress: withdrawalAddress,
        feeQuote: feeQuote!,
        exitSpeed: ExitSpeed.FAST,
        deductFeeFromWithdrawalAmount: true,
      }),
    ).rejects.toMatchObject({
      name: SparkValidationError.name,
      message: expect.stringContaining(
        "The fee for the withdrawal is greater than the target withdrawal amount",
      ),
      context: expect.objectContaining({
        field: "fee",
        expected: "less than or equal to the target amount",
      }),
    });
  }, 600000);

  it("should fail with invalid exitSpeed", async () => {
    const feeQuote = await userWallet.getWithdrawalFeeQuote({
      amountSats: 5000,
      withdrawalAddress,
    });

    await expect(
      userWallet.withdraw({
        amountSats: 5000,
        onchainAddress: withdrawalAddress,
        feeQuote: feeQuote!,
        exitSpeed: "INVALID" as ExitSpeed,
        deductFeeFromWithdrawalAmount: false,
      }),
    ).rejects.toMatchObject({
      name: SparkValidationError.name,
      message: expect.stringContaining("Invalid exit speed"),
      context: expect.objectContaining({
        field: "exitSpeed",
        value: "INVALID" as ExitSpeed,
        expected: "FAST, MEDIUM, or SLOW",
      }),
    });
  }, 600000);

  it("should fail if fee exceeds available balance (without deduction)", async () => {
    await retryUntilSuccess(async () => {
      const { balance } = await userWallet.getBalance();
      if (balance === BigInt(quoteAmount)) return balance;
      throw new Error("Balance incorrect value");
    });

    const initialBalance = (await userWallet.getBalance()).balance;

    const feeQuote = await userWallet.getWithdrawalFeeQuote({
      amountSats: Number(initialBalance),
      withdrawalAddress,
    });

    await expect(
      userWallet.withdraw({
        amountSats: Number(initialBalance) + 1,
        onchainAddress: withdrawalAddress,
        feeQuote: feeQuote!,
        exitSpeed: ExitSpeed.FAST,
        deductFeeFromWithdrawalAmount: true,
      }),
    ).rejects.toThrow("Total target amount exceeds available balance");
  }, 600000);

  // it("should correctly update balance after successful withdrawal", async () => {
  //   const initialBalance = (await userWallet.getBalance()).balance;

  //   const feeQuote = await userWallet.getWithdrawalFeeQuote({
  //     amountSats: 3000,
  //     withdrawalAddress,
  //   });

  //   const result = await userWallet.withdraw({
  //     amountSats: 3000,
  //     onchainAddress: withdrawalAddress,
  //     feeQuote: feeQuote!,
  //     exitSpeed: ExitSpeed.SLOW,
  //     deductFeeFromWithdrawalAmount: false,
  //   });

  //   await new Promise((resolve) => setTimeout(resolve, 30000));

  //   const finalBalance = (await userWallet.getBalance()).balance;
  //   const fee =
  //     (result?.l1BroadcastFee?.originalValue ?? 0) +
  //     (result?.fee?.originalValue ?? 0);

  //   expect(finalBalance).toBe(initialBalance - 3000n - BigInt(fee));
  // }, 600000);
});
