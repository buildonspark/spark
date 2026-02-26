// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test } = require("../utils.js");

test("initialize new wallet and get details", async (assert) => {
  const { wallet, mnemonic } = await SparkWallet.initialize({
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  });

  try {
    assert(typeof mnemonic, "string", "mnemonic is a string");
    assert(mnemonic.split(" ").length >= 12, true, "mnemonic has >= 12 words");

    const { balance } = await wallet.getBalance();
    assert(balance, 0n, "new wallet has zero balance");

    const sparkAddress = await wallet.getSparkAddress();
    assert(typeof sparkAddress, "string", "spark address is a string");
    assert(sparkAddress.length > 0, true, "spark address is non-empty");
  } finally {
    await wallet.cleanupConnections();
  }
});

test("initialize wallet from mnemonic is deterministic", async (assert) => {
  const { wallet: w1, mnemonic } = await SparkWallet.initialize({
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  });

  const { wallet: w2 } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonic,
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  });

  try {
    const addr1 = await w1.getSparkAddress();
    const addr2 = await w2.getSparkAddress();
    assert(addr1, addr2, "same mnemonic produces same spark address");
  } finally {
    await w1.cleanupConnections();
    await w2.cleanupConnections();
  }
});
