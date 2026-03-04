// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test, retryUntilSuccess } = require("../../utils.js");
const { fundWalletSSP } = require("../../fund-wallet-ssp.js");

const DEPOSIT_AMOUNT = 10000n;
const INVOICE_AMOUNT = 1000;

test("ssp-lightning: create and pay lightning invoice via SSP", async (assert) => {
  const opts = {
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  };
  console.log("[lightning] SparkWallet.initialize() for alice...");
  const { wallet: alice } = await SparkWallet.initialize(opts);
  console.log("[lightning] SparkWallet.initialize() for bob...");
  const { wallet: bob } = await SparkWallet.initialize(opts);
  console.log("[lightning] both wallets initialized");

  try {
    // Fund Alice via SSP static deposit flow
    console.log(
      "[lightning] fundWalletSSP(alice, %s)...",
      String(DEPOSIT_AMOUNT),
    );
    const { balance: aliceBalance } = await fundWalletSSP(
      alice,
      DEPOSIT_AMOUNT,
    );
    console.log(
      "[lightning] alice balance after funding:",
      String(aliceBalance),
    );
    assert(aliceBalance > 0n, true, "alice was funded via SSP");

    // Bob creates Lightning invoice via SSP
    console.log(
      "[lightning] bob.createLightningInvoice(%d sats)...",
      INVOICE_AMOUNT,
    );
    const invoiceResult = await bob.createLightningInvoice({
      amountSats: INVOICE_AMOUNT,
      memo: "bare ssp lightning test",
      expirySeconds: 600,
    });
    console.log(
      "[lightning] invoice created:",
      invoiceResult?.invoice?.encodedInvoice?.slice(0, 40) + "...",
    );
    assert(!!invoiceResult, true, "invoice result returned");
    assert(!!invoiceResult.invoice, true, "invoice object present");
    assert(
      typeof invoiceResult.invoice.encodedInvoice,
      "string",
      "encoded invoice is string",
    );
    assert(
      invoiceResult.invoice.encodedInvoice.length > 0,
      true,
      "encoded invoice is non-empty",
    );

    // Alice pays the Lightning invoice via SSP
    console.log("[lightning] alice.payLightningInvoice()...");
    await alice.payLightningInvoice({
      invoice: invoiceResult.invoice.encodedInvoice,
      maxFeeSats: 100,
    });
    console.log("[lightning] payment sent");

    // Verify Bob received the payment
    console.log("[lightning] waiting for bob to receive payment...");
    const { balance: bobBalance } = await retryUntilSuccess(async () => {
      const res = await bob.getBalance();
      if (res.balance <= 0n) {
        throw new Error("Bob balance still zero, retrying...");
      }
      return res;
    });
    console.log("[lightning] bob balance:", String(bobBalance));
    assert(bobBalance, BigInt(INVOICE_AMOUNT), "bob received invoice amount");

    // Verify Alice's balance decreased (includes Lightning routing fees)
    const { balance: aliceFinal } = await alice.getBalance();
    console.log("[lightning] alice final balance:", String(aliceFinal));
    assert(aliceFinal < aliceBalance, true, "alice balance decreased");
  } finally {
    await alice.cleanupConnections();
    await bob.cleanupConnections();
  }
});
