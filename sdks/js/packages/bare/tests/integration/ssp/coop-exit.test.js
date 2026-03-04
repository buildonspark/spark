// Import @buildonspark/bare first to initialize fetch, crypto, and FROST bindings
const { SparkWallet } = require("@buildonspark/bare");
const { test } = require("../../utils.js");
const { BitcoinFaucet } = require("../../bare-faucet.js");
const { fundWalletSSP } = require("../../fund-wallet-ssp.js");

const DEPOSIT_AMOUNT = 30000n;

test("ssp-coop-exit: withdraw via cooperative exit", async (assert) => {
  const opts = {
    options: {
      network: "LOCAL",
      optimizationOptions: { auto: false },
      tokenOptimizationOptions: { enabled: false },
    },
  };
  console.log("[coop-exit] SparkWallet.initialize()...");
  const { wallet } = await SparkWallet.initialize(opts);
  console.log("[coop-exit] wallet initialized");

  try {
    // Fund wallet via SSP static deposit
    console.log("[coop-exit] fundWalletSSP(%s)...", String(DEPOSIT_AMOUNT));
    const { creditAmountSats } = await fundWalletSSP(wallet, DEPOSIT_AMOUNT);
    console.log("[coop-exit] creditAmountSats:", creditAmountSats);
    assert(creditAmountSats > 0, true, "wallet funded via SSP");

    const faucet = BitcoinFaucet.getInstance();
    const withdrawalAddress = await faucet.getNewAddress();
    console.log("[coop-exit] withdrawalAddress:", withdrawalAddress);

    // Get SSP fee quote for cooperative exit
    console.log("[coop-exit] getWithdrawalFeeQuote(5000 sats)...");
    const feeQuote = await wallet.getWithdrawalFeeQuote({
      amountSats: 5000,
      withdrawalAddress,
    });
    console.log(
      "[coop-exit] feeQuote: l1BroadcastFeeFast=%d, userFeeFast=%d",
      feeQuote.l1BroadcastFeeFast.originalValue,
      feeQuote.userFeeFast.originalValue,
    );
    assert(!!feeQuote, true, "fee quote returned");
    assert(
      feeQuote.l1BroadcastFeeFast.originalValue > 0,
      true,
      "fast broadcast fee > 0",
    );
    assert(feeQuote.userFeeFast.originalValue > 0, true, "fast user fee > 0");

    // Perform cooperative exit via SSP
    console.log("[coop-exit] withdraw(5000 sats, exitSpeed=FAST)...");
    const coopExit = await wallet.withdraw({
      amountSats: 5000,
      onchainAddress: withdrawalAddress,
      feeQuote,
      exitSpeed: "FAST",
      deductFeeFromWithdrawalAmount: false,
    });
    console.log("[coop-exit] coopExitTxid:", coopExit.coopExitTxid);
    assert(!!coopExit, true, "coop exit result returned");
    assert(typeof coopExit.coopExitTxid, "string", "got coop exit txid");

    // Verify balance decreased after withdrawal
    const { balance: balanceAfter } = await wallet.getBalance();
    console.log("[coop-exit] balance after withdrawal:", String(balanceAfter));
    assert(
      balanceAfter < BigInt(creditAmountSats),
      true,
      "balance decreased after withdrawal",
    );
  } finally {
    await wallet.cleanupConnections();
  }
});
