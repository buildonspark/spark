import { describe, expect, it } from "@jest/globals";
import { SparkWalletTesting } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";

const DEPOSIT_AMOUNT = 10_000n;

describe("SSP coop exit integration", () => {
  it("should estimate coop exit fee", async () => {
    const faucet = new BitcoinFaucet();

    const { wallet: userWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    const depositAddress = await userWallet.getDepositAddress();
    expect(depositAddress).toBeDefined();

    const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);
    expect(signedTx).toBeDefined();
    await faucet.mineBlocks(6);

    await userWallet.claimDeposit(signedTx.id);

    const { balance } = await userWallet.getBalance();
    expect(balance).toBe(DEPOSIT_AMOUNT);

    const withdrawalAddress = await faucet.getNewAddress();

    const feeEstimate = await userWallet.getCoopExitFeeEstimate({
      amountSats: Number(DEPOSIT_AMOUNT),
      withdrawalAddress,
    });

    expect(feeEstimate).toBeDefined();
    expect(feeEstimate?.feeEstimate).toBeDefined();
    expect(feeEstimate?.feeEstimate.originalValue).toBeGreaterThan(0);
  }, 60000);
});
