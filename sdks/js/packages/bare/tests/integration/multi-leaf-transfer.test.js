// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test, retryUntilSuccess } = require("../utils.js");
const { fundWallet } = require("../fund-wallet.js");

test("multi-leaf-transfer: transfer spanning multiple leaves", async (assert) => {
  const opts = {
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  };
  const { wallet: sender } = await SparkWallet.initialize(opts);
  const { wallet: receiver } = await SparkWallet.initialize(opts);

  try {
    // Fund with two separate deposits to create two distinct leaves
    const balance1 = await fundWallet(sender, 50000n);
    assert(balance1 > 0n, true, "first deposit credited");

    const balance2 = await fundWallet(sender, 50000n);
    assert(balance2 > balance1, true, "second deposit credited");

    const { balance: senderTotal } = await sender.getBalance();
    assert(senderTotal > 0n, true, "sender has positive total balance");

    const receiverAddress = await receiver.getSparkAddress();
    const transferAmount = Number(senderTotal);
    // Retry the transfer because deposited leaves may still be in CREATING
    // status on the SO (waiting for on-chain confirmation) even though the
    // SDK balance already reflects them. The SO rejects transfers on
    // CREATING leaves with FAILED_PRECONDITION, so retry until they
    // transition to AVAILABLE.
    const transferResult = await retryUntilSuccess(
      () =>
        sender.transfer({
          receiverSparkAddress: receiverAddress,
          amountSats: transferAmount,
        }),
      { maxAttempts: 15, delayMs: 2000 },
    );
    assert(!!transferResult, true, "transfer returned result");

    await new Promise((r) => setTimeout(r, 5000));

    const { balance: receiverBalance } = await retryUntilSuccess(async () => {
      const result = await receiver.getBalance();
      if (result.balance <= 0n) {
        throw new Error("Receiver balance still zero, retrying...");
      }
      return result;
    });
    assert(
      receiverBalance,
      BigInt(transferAmount),
      "receiver got full multi-leaf amount",
    );

    const { balance: senderFinal } = await sender.getBalance();
    assert(senderFinal, 0n, "sender balance is zero after full transfer");
  } finally {
    await sender.cleanupConnections();
    await receiver.cleanupConnections();
  }
});
