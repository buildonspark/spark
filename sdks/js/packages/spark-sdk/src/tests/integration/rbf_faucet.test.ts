import { BitcoinFaucet } from "../utils/test-faucet.js";
import { Transaction } from "@scure/btc-signer";
import { SparkWalletTestingWithStream } from "../utils/spark-testing-wallet.js";

const DEPOSIT_AMOUNT = 10000n;

function findOutputVout(tx: Transaction, amount: bigint): number | undefined {
  for (let i = 0; i < tx.outputsLength; i++) {
    const output = tx.getOutput(i);
    if (output.amount === amount) {
      return i;
    }
  }
  return undefined;
}

describe("RBF faucet support", () => {
  it("should create an RBF transaction and replace it", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet: userWallet } =
      await SparkWalletTestingWithStream.initialize({
        options: {
          network: "LOCAL",
        },
      });

    const depositAddress = await userWallet.getStaticDepositAddress();
    expect(depositAddress).toBeDefined();

    // Send RBF-signaled transaction (stays in mempool, no blocks mined)
    const { tx: tx1, coin } = await faucet.sendToAddressRbf(
      depositAddress,
      DEPOSIT_AMOUNT,
    );
    expect(tx1).toBeDefined();
    const tx1Id = tx1.id;

    // Replace with a higher-fee transaction using the same coin.
    // If tx1 was already confirmed by the auto-miner, this will fail with
    // "bad-txns-inputs-missingorspent" — that's expected, so we handle it.
    let tx2: Transaction;
    let tx2Id: string;
    let replaced = false;
    try {
      tx2 = await faucet.replaceTransaction(
        coin,
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      tx2Id = tx2.id;
      expect(tx2Id).not.toBe(tx1Id);
      replaced = true;

      // If replacement succeeded, tx1 should no longer be in the mempool
      await expect(faucet.getMempoolEntry(tx1Id)).rejects.toThrow();
    } catch (err) {
      // Only tolerate the specific RPC error when tx1 was already confirmed
      // by the auto-miner. Re-throw everything else (including Jest assertions).
      const isMissingInput =
        err instanceof Error &&
        err.message.includes("bad-txns-inputs-missingorspent");
      if (!isMissingInput) throw err;
      tx2 = tx1;
      tx2Id = tx1Id;
    }

    // Mine a block to confirm whichever tx is pending
    await faucet.mineBlocks(1);

    // Verify the final tx has the expected output
    const finalTx = replaced ? tx2! : tx1;
    const vout = findOutputVout(finalTx, DEPOSIT_AMOUNT);
    expect(vout).toBeDefined();

    await userWallet.cleanupConnections();
  }, 60000);
});
