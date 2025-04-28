import { describe, expect, it } from "@jest/globals";
import { SparkWalletTesting } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";

const DEPOSIT_AMOUNT = 10_000n;

describe("SSP coop exit integration", () => {
  it("should estimate coop exit fee", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: userWallet } = await SparkWalletTesting.initialize(
      {
        options: {
          network: "LOCAL",
        },
      },
      false,
    );

    const depositAddress = await userWallet.getSingleUseDepositAddress();
    expect(depositAddress).toBeDefined();

    const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);
    expect(signedTx).toBeDefined();
    await faucet.mineBlocks(6);

    await userWallet.claimDeposit(signedTx.id);

    await new Promise((resolve) => setTimeout(resolve, 1000));

    const { balance } = await userWallet.getBalance();
    expect(balance).toBe(DEPOSIT_AMOUNT);

    const withdrawalAddress = await faucet.getNewAddress();

    const feeEstimate = await userWallet.getWithdrawalFeeEstimate({
      amountSats: Number(DEPOSIT_AMOUNT),
      withdrawalAddress,
    });

    expect(feeEstimate).toBeDefined();
    expect(feeEstimate?.speedFast?.l1BroadcastFee).toBeDefined();
    expect(
      feeEstimate?.speedFast?.l1BroadcastFee.originalValue,
    ).toBeGreaterThan(0);
    expect(feeEstimate?.speedFast?.userFee).toBeDefined();
    expect(feeEstimate?.speedFast?.userFee.originalValue).toBeGreaterThan(0);

    expect(feeEstimate?.speedMedium?.l1BroadcastFee).toBeDefined();
    expect(
      feeEstimate?.speedMedium?.l1BroadcastFee.originalValue,
    ).toBeGreaterThan(0);
    expect(feeEstimate?.speedMedium?.userFee).toBeDefined();
    expect(feeEstimate?.speedMedium?.userFee.originalValue).toBeGreaterThan(0);

    expect(feeEstimate?.speedSlow?.l1BroadcastFee).toBeDefined();
    expect(
      feeEstimate?.speedSlow?.l1BroadcastFee.originalValue,
    ).toBeGreaterThan(0);
    expect(feeEstimate?.speedSlow?.userFee).toBeDefined();
    expect(feeEstimate?.speedSlow?.userFee.originalValue).toBeGreaterThan(0);
  }, 60000);
});
