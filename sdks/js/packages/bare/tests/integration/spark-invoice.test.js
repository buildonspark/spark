// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test, retryUntilSuccess } = require("../utils.js");
const { fundWallet } = require("../fund-wallet.js");

// Fund with exact amount to avoid leaf-swap via SSP (not deployed in hermetic env).
const INVOICE_AMOUNT = 1000;
const DEPOSIT_AMOUNT = BigInt(INVOICE_AMOUNT);

test("spark-invoice: create and fulfill invoice", async (assert) => {
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
    const creditedAmount = await fundWallet(sender, DEPOSIT_AMOUNT);
    assert(creditedAmount, DEPOSIT_AMOUNT, "sender was funded");

    const tomorrow = new Date(Date.now() + 1000 * 60 * 60 * 24);
    const invoice = await receiver.createSatsInvoice({
      amount: INVOICE_AMOUNT,
      memo: "test invoice",
      expiryTime: tomorrow,
    });
    assert(typeof invoice, "string", "invoice is a string");
    assert(invoice.length > 0, true, "invoice is non-empty");

    const result = await sender.fulfillSparkInvoice([{ invoice }]);
    assert(
      result.satsTransactionSuccess.length,
      1,
      "one successful transaction",
    );
    assert(result.satsTransactionErrors.length, 0, "no transaction errors");
    assert(result.invalidInvoices.length, 0, "no invalid invoices");

    const { balance: receiverBalance } = await retryUntilSuccess(async () => {
      const res = await receiver.getBalance();
      if (res.balance <= 0n) {
        throw new Error("Receiver balance still zero, retrying...");
      }
      return res;
    });
    assert(receiverBalance, DEPOSIT_AMOUNT, "receiver got full amount");

    const { balance: senderBalance } = await sender.getBalance();
    assert(senderBalance, 0n, "sender balance is zero after full transfer");
  } finally {
    await sender.cleanupConnections();
    await receiver.cleanupConnections();
  }
});
