/**
 * Shared wallet funding helper for bare SSP integration tests.
 *
 * Uses static deposit addresses + SSP claim quote flow (requires SSP).
 * Adapted from spark-sdk/src/tests/integration/ssp/static_deposit.test.ts.
 */

const { retryUntilSuccess } = require("./utils.js");
const { BitcoinFaucet } = require("./bare-faucet.js");

async function fundWalletSSP(wallet, amount = 10000n) {
  const faucet = BitcoinFaucet.getInstance();

  console.log("[fundWalletSSP] getStaticDepositAddress()...");
  const depositAddress = await wallet.getStaticDepositAddress();
  console.log("[fundWalletSSP] depositAddress:", depositAddress);
  if (!depositAddress) {
    throw new Error("Failed to get static deposit address");
  }

  console.log(
    "[fundWalletSSP] sendToAddress(%s, %s)...",
    depositAddress,
    String(amount),
  );
  const signedTx = await faucet.sendToAddress(depositAddress, amount);
  console.log("[fundWalletSSP] txid:", signedTx.id);
  await faucet.mineBlocks(6);
  console.log("[fundWalletSSP] mined 6 blocks");

  const transactionId = signedTx.id;

  // Find the output index matching our deposit amount
  let vout;
  for (let i = 0; i < signedTx.outputsLength; i++) {
    const output = signedTx.getOutput(i);
    if (output.amount === amount) {
      vout = i;
      break;
    }
  }
  console.log("[fundWalletSSP] output vout:", vout);
  if (vout === undefined) {
    throw new Error(
      `No output found matching amount ${amount} in transaction ${transactionId}`,
    );
  }

  // Get SSP claim quote
  console.log(
    "[fundWalletSSP] getClaimStaticDepositQuote(%s, %d)...",
    transactionId,
    vout,
  );
  const quote = await retryUntilSuccess(async () => {
    const q = await wallet.getClaimStaticDepositQuote(transactionId, vout);
    if (!q) throw new Error("Quote not available yet");
    return q;
  });
  console.log(
    "[fundWalletSSP] quote: creditAmountSats=%d, signature=%s",
    quote.creditAmountSats,
    quote.signature?.slice(0, 20) + "...",
  );

  // Claim via SSP
  console.log("[fundWalletSSP] claimStaticDeposit()...");
  await retryUntilSuccess(
    async () =>
      await wallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quote.creditAmountSats,
        sspSignature: quote.signature,
        outputIndex: vout,
      }),
  );
  console.log("[fundWalletSSP] claim succeeded");

  // Wait for claim to be reflected in balance
  const { balance } = await retryUntilSuccess(async () => {
    const res = await wallet.getBalance();
    if (res.balance <= 0n) throw new Error("Balance still zero, retrying...");
    return res;
  });
  console.log("[fundWalletSSP] final balance:", String(balance));
  return { balance, creditAmountSats: quote.creditAmountSats };
}

module.exports = { fundWalletSSP };
