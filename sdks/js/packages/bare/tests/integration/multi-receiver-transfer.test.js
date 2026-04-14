// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test, retryUntilSuccess } = require("../utils.js");
const { fundWallet } = require("../fund-wallet.js");

test("multi-receiver-transfer: send to two receivers via transferV2", async (assert) => {
  const opts = {
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  };
  const { wallet: sender } = await SparkWallet.initialize(opts);
  const { wallet: receiver1 } = await SparkWallet.initialize(opts);
  const { wallet: receiver2 } = await SparkWallet.initialize(opts);

  try {
    // Fund sender with two deposits to have enough leaves for two receivers
    const balance1 = await fundWallet(sender, 50000n);
    assert(balance1 > 0n, true, "first deposit credited");

    const balance2 = await fundWallet(sender, 50000n);
    assert(balance2 > balance1, true, "second deposit credited");

    const sparkAddr1 = await receiver1.getSparkAddress();
    const sparkAddr2 = await receiver2.getSparkAddress();

    // Retry the multi-receiver transfer because deposited leaves may still
    // be in CREATING status on the SO.
    const transfer = await retryUntilSuccess(
      () =>
        sender.transferV2({
          receivers: [
            { receiverSparkAddress: sparkAddr1, amountSats: 50000 },
            { receiverSparkAddress: sparkAddr2, amountSats: 50000 },
          ],
        }),
      { maxAttempts: 15, delayMs: 2000 },
    );
    assert(!!transfer.id, true, "transfer returned an id");

    const { balance: senderAfter } = await sender.getBalance();
    assert(senderAfter, 0n, "sender balance is zero after transfer");

    // Both receivers should eventually see their balance
    const { balance: r1Balance } = await retryUntilSuccess(async () => {
      const result = await receiver1.getBalance();
      if (result.balance <= 0n) {
        throw new Error("Receiver 1 balance still zero, retrying...");
      }
      return result;
    });
    assert(r1Balance, 50000n, "receiver 1 got 50000 sats");

    const { balance: r2Balance } = await retryUntilSuccess(async () => {
      const result = await receiver2.getBalance();
      if (result.balance <= 0n) {
        throw new Error("Receiver 2 balance still zero, retrying...");
      }
      return result;
    });
    assert(r2Balance, 50000n, "receiver 2 got 50000 sats");
  } finally {
    await sender.cleanupConnections();
    await receiver1.cleanupConnections();
    await receiver2.cleanupConnections();
  }
});
