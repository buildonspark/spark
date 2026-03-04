// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test, retryUntilSuccess } = require("../../utils.js");
const { BitcoinFaucet } = require("../../bare-faucet.js");

const DEPOSIT_AMOUNT = 10000n;

test("ssp-static-deposit: claim deposit via SSP quote", async (assert) => {
  const opts = {
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  };
  console.log("[static-deposit] SparkWallet.initialize()...");
  const { wallet } = await SparkWallet.initialize(opts);
  console.log("[static-deposit] wallet initialized");

  try {
    const faucet = BitcoinFaucet.getInstance();

    // Get SSP-registered static deposit address
    console.log("[static-deposit] getStaticDepositAddress()...");
    const depositAddress = await wallet.getStaticDepositAddress();
    console.log("[static-deposit] depositAddress:", depositAddress);
    assert(typeof depositAddress, "string", "got static deposit address");
    assert(depositAddress.length > 0, true, "deposit address is non-empty");

    // Fund the address via regtest faucet
    console.log(
      "[static-deposit] sendToAddress(%s, %s)...",
      depositAddress,
      String(DEPOSIT_AMOUNT),
    );
    const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);
    console.log("[static-deposit] txid:", signedTx.id);
    await faucet.mineBlocks(6);
    console.log("[static-deposit] mined 6 blocks");

    const transactionId = signedTx.id;
    assert(typeof transactionId, "string", "got transaction id");

    // Find the output index matching our deposit amount
    let vout;
    for (let i = 0; i < signedTx.outputsLength; i++) {
      const output = signedTx.getOutput(i);
      if (output.amount === DEPOSIT_AMOUNT) {
        vout = i;
        break;
      }
    }
    console.log("[static-deposit] output vout:", vout);
    assert(vout !== undefined, true, "found matching output index");

    // Get SSP claim quote
    console.log(
      "[static-deposit] getClaimStaticDepositQuote(%s, %d)...",
      transactionId,
      vout,
    );
    const quote = await retryUntilSuccess(async () => {
      const q = await wallet.getClaimStaticDepositQuote(transactionId, vout);
      if (!q) throw new Error("Quote not available yet");
      return q;
    });
    console.log(
      "[static-deposit] quote: creditAmountSats=%d, signature=%s",
      quote.creditAmountSats,
      quote.signature?.slice(0, 20) + "...",
    );
    assert(
      quote.creditAmountSats > 0,
      true,
      "quote has positive credit amount",
    );
    assert(typeof quote.signature, "string", "quote has SSP signature");

    // Claim via SSP
    console.log("[static-deposit] claimStaticDeposit()...");
    await wallet.claimStaticDeposit({
      transactionId,
      creditAmountSats: quote.creditAmountSats,
      sspSignature: quote.signature,
      outputIndex: vout,
    });
    console.log("[static-deposit] claim succeeded");

    // Verify balance matches quote
    console.log("[static-deposit] getBalance()...");
    const { balance } = await retryUntilSuccess(async () => {
      const res = await wallet.getBalance();
      if (res.balance <= 0n) throw new Error("Balance still zero, retrying...");
      return res;
    });
    console.log("[static-deposit] balance:", String(balance));
    assert(
      balance,
      BigInt(quote.creditAmountSats),
      "balance matches SSP quote",
    );
  } finally {
    await wallet.cleanupConnections();
  }
});
