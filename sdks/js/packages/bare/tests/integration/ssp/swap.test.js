// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test } = require("../../utils.js");
// Uses SO-only funding (not SSP) to create a single leaf, so the non-exact
// transfer forces the SDK to swap with SSP for change.
const { fundWallet } = require("../../fund-wallet.js");

const DEPOSIT_AMOUNT = 10000n;

test("ssp-swap: SSP swap triggered when transferring non-exact amount", async (assert) => {
  const opts = {
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  };
  console.log("[swap] SparkWallet.initialize()...");
  const { wallet } = await SparkWallet.initialize(opts);
  console.log("[swap] wallet initialized");

  try {
    // Fund wallet via direct SO deposit (creates a single 10000 sat leaf)
    console.log("[swap] fundWallet(%s)...", String(DEPOSIT_AMOUNT));
    const creditedAmount = await fundWallet(wallet, DEPOSIT_AMOUNT);
    console.log("[swap] creditedAmount:", String(creditedAmount));
    assert(creditedAmount, DEPOSIT_AMOUNT, "wallet was funded");

    // Transfer a non-exact amount (8191 sats from a 10000 sat leaf).
    // The SDK must swap with SSP to make change, since no exact leaf exists.
    const sparkAddress = await wallet.getSparkAddress();
    console.log("[swap] sparkAddress:", sparkAddress);
    console.log("[swap] transfer(8191 sats) — should trigger SSP swap...");
    await wallet.transfer({
      amountSats: 8191,
      receiverSparkAddress: sparkAddress,
    });
    console.log("[swap] transfer with SSP swap succeeded");

    // After self-transfer with SSP swap, balance should be close to original
    // (SSP swap fees are currently zero in LOCAL but may change)
    const { balance } = await wallet.getBalance();
    console.log("[swap] balance after self-transfer:", String(balance));
    assert(balance > 0n, true, "balance positive after self-transfer");
    assert(balance <= DEPOSIT_AMOUNT, true, "balance not more than deposited");
  } finally {
    await wallet.cleanupConnections();
  }
});
