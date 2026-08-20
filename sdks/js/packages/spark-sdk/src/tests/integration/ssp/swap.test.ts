import { describe, expect, it } from "@jest/globals";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";

const DEPOSIT_AMOUNT = 10000n;

/** Poll a condition until it's true or timeout. */
async function waitFor(
  fn: () => Promise<boolean>,
  timeoutMs: number = 15000,
  intervalMs: number = 500,
): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (await fn()) return;
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  throw new Error(`waitFor timed out after ${timeoutMs}ms`);
}

describe("SSP swap", () => {
  it("concurrent wallet should reflect correct balance after the other wallet swaps", async () => {
    const faucet = BitcoinFaucet.getInstance();

    // Wallet A — will perform the swap
    const { wallet: walletA, mnemonic } =
      await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

    try {
      const depositAddress = await walletA.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(3);
      await walletA.claimDeposit(signedTx.id);

      const { satsBalance: preBalance } = await walletA.getBalance();
      expect(preBalance.available).toBe(DEPOSIT_AMOUNT);
      expect(preBalance.owned).toBe(DEPOSIT_AMOUNT);

      // Wallet B — same mnemonic, separate instance, initialized BEFORE the swap
      const { wallet: walletB } = await SparkWalletTestingWithStream.initialize(
        {
          mnemonicOrSeed: mnemonic,
          options: { network: "LOCAL" },
        },
      );

      try {
        // Wallet B should see the same starting balance
        const { satsBalance: walletBPre } = await walletB.getBalance();
        expect(walletBPre.available).toBe(DEPOSIT_AMOUNT);
        expect(walletBPre.owned).toBe(DEPOSIT_AMOUNT);

        // Wallet A triggers a swap via self-transfer
        const sparkAddress = await walletA.getSparkAddress();
        await walletA.transfer({
          amountSats: 3000,
          receiverSparkAddress: sparkAddress,
        });

        // Wallet A should be correct immediately
        const { satsBalance: walletAPost } = await walletA.getBalance();
        expect(walletAPost.available).toBe(DEPOSIT_AMOUNT);
        expect(walletAPost.owned).toBe(DEPOSIT_AMOUNT);

        // Wallet B should eventually converge to the correct balance.
        // The owned balance should never exceed the deposit amount (no doubling).
        await waitFor(async () => {
          const { satsBalance } = await walletB.getBalance();
          return satsBalance.available === DEPOSIT_AMOUNT;
        });

        const { satsBalance: walletBFinal } = await walletB.getBalance();
        expect(walletBFinal.available).toBe(DEPOSIT_AMOUNT);
        expect(walletBFinal.owned).toBe(DEPOSIT_AMOUNT);
      } finally {
        await walletB.cleanupConnections();
      }
    } finally {
      await walletA.cleanupConnections();
    }
  }, 90000);
});
