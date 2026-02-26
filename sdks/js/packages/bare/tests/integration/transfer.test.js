// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test, retryUntilSuccess } = require("../utils.js");
const { fundWallet } = require("../fund-wallet.js");

const DEPOSIT_AMOUNT = 100000n;

test("transfer: send sats between two wallets", async (assert) => {
  const opts = {
    options: {
      network: "LOCAL",
      // SSP is not deployed in the hermetic test environment,
      // so disable features that require it.
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  };
  const { wallet: sender } = await SparkWallet.initialize(opts);
  const { wallet: receiver } = await SparkWallet.initialize(opts);

  try {
    const creditedAmount = await fundWallet(sender, DEPOSIT_AMOUNT);
    assert(creditedAmount > 0n, true, "sender was funded");

    const receiverAddress = await receiver.getSparkAddress();
    assert(typeof receiverAddress, "string", "receiver address is string");

    const transferAmount = Number(creditedAmount);
    const transferResult = await sender.transfer({
      receiverSparkAddress: receiverAddress,
      amountSats: transferAmount,
    });
    assert(!!transferResult, true, "transfer returned result");

    // Wait for transfer to settle
    await new Promise((r) => setTimeout(r, 5000));

    const { balance: receiverBalance } = await retryUntilSuccess(async () => {
      const result = await receiver.getBalance();
      if (result.balance <= 0n) {
        throw new Error("Receiver balance still zero, retrying...");
      }
      return result;
    });
    assert(receiverBalance > 0n, true, "receiver has positive balance");
    assert(
      receiverBalance,
      BigInt(transferAmount),
      "receiver balance matches transfer amount",
    );

    const { balance: senderBalance } = await sender.getBalance();
    assert(senderBalance, 0n, "sender balance is zero after full transfer");
  } finally {
    await sender.cleanupConnections();
    await receiver.cleanupConnections();
  }
});
