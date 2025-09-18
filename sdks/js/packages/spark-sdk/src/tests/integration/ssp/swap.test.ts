import { describe, expect, it } from "@jest/globals";
import { SparkWalletTesting } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";

const DEPOSIT_AMOUNT = 10000n;

describe("SSP swap", () => {
  it("it should swap with the SSP before sending a transfer if the user does not have exact leaf amount", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: userWallet } =
      await SparkWalletTestingWithStream.initialize({
        options: {
          network: "LOCAL",
        },
      });

    const depositAddress = await userWallet.getSingleUseDepositAddress();
    expect(depositAddress).toBeDefined();

    const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);

    // Wait for the transaction to be mined
    await faucet.mineBlocks(6);

    expect(signedTx).toBeDefined();

    const transactionId = signedTx.id;

    await userWallet.claimDeposit(transactionId);

    const { balance } = await userWallet.getBalance();
    expect(balance).toBe(DEPOSIT_AMOUNT);

    const sparkAddress = await userWallet.getSparkAddress();

    await userWallet.transfer({
      amountSats: 8191,
      receiverSparkAddress: sparkAddress,
    });

    const { balance: receiverBalance } = await userWallet.getBalance();
    expect(receiverBalance).toBe(balance);
  }, 60000);
});
