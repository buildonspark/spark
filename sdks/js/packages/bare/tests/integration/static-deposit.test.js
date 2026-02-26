// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test, retryUntilSuccess } = require("../utils.js");
const { BitcoinFaucet } = require("../bare-faucet.js");

const DEPOSIT_AMOUNT = 100000n;

test("deposit: fund via single-use address, claim, check balance", async (assert) => {
  const faucet = BitcoinFaucet.getInstance();

  const { wallet } = await SparkWallet.initialize({
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  });

  try {
    const depositAddress = await wallet.getSingleUseDepositAddress();
    assert(typeof depositAddress, "string", "deposit address is string");
    assert(depositAddress.length > 0, true, "deposit address is non-empty");

    const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);
    await faucet.mineBlocks(3);

    const transactionId = signedTx.id;
    assert(typeof transactionId, "string", "transaction id is string");

    await retryUntilSuccess(() => wallet.claimDeposit(transactionId));

    await new Promise((r) => setTimeout(r, 3000));

    const { balance } = await wallet.getBalance();
    assert(balance > 0n, true, "balance is positive after deposit");
  } finally {
    await wallet.cleanupConnections();
  }
});
