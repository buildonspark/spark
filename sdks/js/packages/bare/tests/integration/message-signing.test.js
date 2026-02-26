// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test } = require("../utils.js");

const WALLET_OPTS = {
  options: {
    network: "LOCAL",
    optimizationOptions: { auto: false },
    tokenOptimizationOptions: { enabled: false },
  },
};

test("message-signing: sign and validate message", async (assert) => {
  const { wallet } = await SparkWallet.initialize(WALLET_OPTS);

  try {
    const message = "Hello, Spark!";
    const signature = await wallet.signMessageWithIdentityKey(message);
    assert(typeof signature, "string", "signature is a string");
    assert(signature.length > 0, true, "signature is non-empty");

    const isValid = await wallet.validateMessageWithIdentityKey(
      message,
      signature,
    );
    assert(isValid, true, "signature validates correctly");
  } finally {
    await wallet.cleanupConnections();
  }
});

test("message-signing: compact vs non-compact signatures differ", async (assert) => {
  const { wallet } = await SparkWallet.initialize(WALLET_OPTS);

  try {
    const message = "test";
    const sigNonCompact = await wallet.signMessageWithIdentityKey(
      message,
      false,
    );
    const sigCompact = await wallet.signMessageWithIdentityKey(message, true);
    assert(sigNonCompact !== sigCompact, true, "signatures differ");

    const validNonCompact = await wallet.validateMessageWithIdentityKey(
      message,
      sigNonCompact,
    );
    assert(validNonCompact, true, "non-compact signature validates");

    const validCompact = await wallet.validateMessageWithIdentityKey(
      message,
      sigCompact,
    );
    assert(validCompact, true, "compact signature validates");
  } finally {
    await wallet.cleanupConnections();
  }
});

test("message-signing: cross-wallet validation fails", async (assert) => {
  const { wallet: wallet1 } = await SparkWallet.initialize(WALLET_OPTS);
  const { wallet: wallet2 } = await SparkWallet.initialize(WALLET_OPTS);

  try {
    const message = "Hello, world!";
    const signature = await wallet1.signMessageWithIdentityKey(message);

    const isValid = await wallet2.validateMessageWithIdentityKey(
      message,
      signature,
    );
    assert(isValid, false, "different wallet rejects signature");
  } finally {
    await wallet1.cleanupConnections();
    await wallet2.cleanupConnections();
  }
});

test("message-signing: wrong message validation fails", async (assert) => {
  const { wallet } = await SparkWallet.initialize(WALLET_OPTS);

  try {
    const signature = await wallet.signMessageWithIdentityKey("message A");

    const isValid = await wallet.validateMessageWithIdentityKey(
      "message B",
      signature,
    );
    assert(isValid, false, "wrong message rejects signature");
  } finally {
    await wallet.cleanupConnections();
  }
});
