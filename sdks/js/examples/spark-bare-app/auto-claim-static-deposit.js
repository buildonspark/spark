// Example Bare script using Spark SDK and Frost addon
import { SparkWallet } from "@buildonspark/bare";
import process from "bare-process";
import walletConfig, { getExampleWalletOptions } from "./wallet-config.js";

async function autoclaimStaticDeposit(mnemonicInit, transactionId) {
  const options = getExampleWalletOptions(process.env, "REGTEST");
  let wallet;
  try {
    const initialized = await SparkWallet.initialize({
      mnemonicOrSeed: mnemonicInit,
      options,
    });
    wallet = initialized.wallet;
    const quote = await wallet.getClaimStaticDepositQuote(transactionId);
    return await wallet.claimStaticDeposit({
      transactionId,
      creditAmountSats: quote.creditAmountSats,
      sspSignature: quote.signature,
    });
  } finally {
    await wallet?.cleanup();
  }
}

const args = process.argv.slice(2);
if (args.length !== 1) {
  console.error("Please provide the transaction ID to claim");
  process.exit(1);
}

const config = walletConfig;

if (!config.mnemonic) {
  console.error("No mnemonic provided in wallet-config.js.");
  process.exit(1);
}

const transactionId = args[0];
if (!transactionId) {
  console.error("No transaction ID provided to claim static deposit.");
  process.exit(1);
}

try {
  const claimDepositResult = await autoclaimStaticDeposit(
    config.mnemonic,
    transactionId,
  );
  console.log("Claimed static deposit:", claimDepositResult);
} catch (error) {
  console.error(error);
  process.exit(1);
}
