// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { imports } = require("../utils.js");
const { bytesToHex } = require("@noble/curves/abstract/utils", imports);
const { test, retryUntilSuccess } = require("../utils.js");
const { fundWallet } = require("../fund-wallet.js");

// Fund with exact amount to avoid leaf-swap via SSP (not deployed in hermetic env).
const HTLC_AMOUNT = 1000;
const DEPOSIT_AMOUNT = BigInt(HTLC_AMOUNT);

test("htlc: create and claim with preimage", async (assert) => {
  const opts = {
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  };
  const { wallet: alice } = await SparkWallet.initialize(opts);
  const { wallet: bob } = await SparkWallet.initialize(opts);

  try {
    const creditedAmount = await fundWallet(alice, DEPOSIT_AMOUNT);
    assert(creditedAmount, DEPOSIT_AMOUNT, "alice was funded");

    const bobAddress = await bob.getSparkAddress();
    const htlc = await alice.createHTLC({
      receiverSparkAddress: bobAddress,
      amountSats: HTLC_AMOUNT,
      expiryTime: new Date(Date.now() + 5 * 60 * 1000),
    });
    assert(!!htlc.id, true, "htlc has transfer id");

    const preimage = await alice.getHTLCPreimage(htlc.id);
    assert(preimage instanceof Uint8Array, true, "preimage is Uint8Array");

    const { balance: aliceAfterCreate } = await alice.getBalance();
    assert(aliceAfterCreate, 0n, "alice balance is zero after HTLC creation");

    const preimageHex = bytesToHex(preimage);
    await bob.claimHTLC(preimageHex);

    const { balance: bobBalance } = await retryUntilSuccess(async () => {
      const result = await bob.getBalance();
      if (result.balance <= 0n) {
        throw new Error("Bob balance still zero, retrying...");
      }
      return result;
    });
    assert(bobBalance, DEPOSIT_AMOUNT, "bob received full HTLC amount");

    const { balance: aliceFinal } = await alice.getBalance();
    assert(aliceFinal, 0n, "alice balance is zero");
  } finally {
    await alice.cleanupConnections();
    await bob.cleanupConnections();
  }
});
