// Example Bare script using Spark SDK and Frost addon
import {
  SparkWallet,
  BareSparkSigner,
} from "@buildonspark/bare" with { imports: "./imports.json" };
import process from "bare-process";
import walletConfig from "./wallet-config.js";

async function autoclaimStaticDeposit(mnemonicInit, transactionId) {
  let { wallet, mnemonic } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonicInit,
    signer: new BareSparkSigner(),
    options: {
      network: "REGTEST",
    },
  });
  const quote = await wallet.getClaimStaticDepositQuote(transactionId);
  const claimResult = await wallet.claimStaticDeposit({
    transactionId,
    creditAmountSats: quote.creditAmountSats,
    sspSignature: quote.signature,
  });
  return claimResult;
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
  process.exit(0);
} catch (error) {
  console.error(error);
  process.exit(1);
}
