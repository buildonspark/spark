import { initTestingWallet } from "../utils/spark-testing-wallet.js";
import { retryUntilSuccess } from "../utils/utils.js";

export const DEPOSIT_AMOUNT = 10000n;

describe("SSP static deposit address integration", () => {
  describe("Refund unhappy path testing", () => {
    // TODO: This test case will need to be fixed.
    it.skip("should reject second refund attempt on already refunded deposit", async () => {
      console.log("Initializing wallet for double-refund test...");
      const {
        wallet: userWallet,
        depositAddress,
        signedTx,
        vout,
        faucet,
      } = await initTestingWallet(DEPOSIT_AMOUNT, "LOCAL");

      // Wait for the transaction to be mined
      await faucet.mineBlocksAndWaitForMiningToComplete(6);

      const transactionId = signedTx.id;

      // First refund attempt should succeed
      console.log("Attempting first refund of static deposit...");
      const txHex = await userWallet.refundStaticDeposit({
        depositTransactionId: transactionId,
        outputIndex: vout!,
        destinationAddress: depositAddress,
        fee: 301,
      });

      await faucet.broadcastTx(txHex);

      await faucet.mineBlocksAndWaitForMiningToComplete(6);

      // Second refund attempt should fail
      console.log(
        "Expecting error when attempting a second refund on same deposit...",
      );
      await expect(
        userWallet.refundStaticDeposit({
          depositTransactionId: transactionId,
          outputIndex: vout!,
          destinationAddress: depositAddress,
          fee: 301,
        }),
      ).rejects.toThrow();
    }, 600000);

    // Skipping because chainwatcher is not catching up to the transaction.
    it.skip("should refund and broadcast a static deposit refund transaction", async () => {
      const {
        wallet: userWallet,
        depositAddress,
        signedTx,
        vout,
        faucet,
      } = await initTestingWallet(DEPOSIT_AMOUNT, "LOCAL");

      // Wait for the transaction to be mined
      await faucet.mineBlocksAndWaitForMiningToComplete(6);

      const transactionId = signedTx.id;

      const txId = await retryUntilSuccess(
        async () =>
          await userWallet.refundAndBroadcastStaticDeposit({
            depositTransactionId: transactionId,
            outputIndex: vout!,
            destinationAddress: depositAddress,
            satsPerVbyteFee: 2,
          }),
      );

      expect(txId).toBeDefined();
    }, 600000);

    // TODO: This is flaky and needs to be fixed.
    it.skip("should fail due to low fee", async () => {
      console.log("Initializing wallet for low-fee refund test...");
      const {
        wallet: userWallet,
        depositAddress,
        signedTx,
        vout,
        faucet,
      } = await initTestingWallet(DEPOSIT_AMOUNT, "LOCAL");

      // Wait for the transaction to be mined
      await faucet.mineBlocksAndWaitForMiningToComplete(6);

      const transactionId = signedTx.id;

      expect(transactionId).toBeDefined();

      // refund attempt should fail due to low fee
      console.log("Expecting error when attempting refund with too low fee...");
      await expect(
        userWallet.refundStaticDeposit({
          depositTransactionId: transactionId,
          outputIndex: vout!,
          destinationAddress: depositAddress,
          fee: 150,
        }),
      ).rejects.toThrow();

      console.log("Attempting refund with sufficient fee...");
      await userWallet.refundStaticDeposit({
        depositTransactionId: transactionId,
        outputIndex: vout!,
        destinationAddress: depositAddress,
        satsPerVbyteFee: 2,
      });
    }, 600000);
  });
});
