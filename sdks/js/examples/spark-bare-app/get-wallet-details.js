// Example Bare script using Spark SDK and Frost addon
import {
  SparkWallet,
  BareSparkSigner,
} from "@buildonspark/bare" with { imports: "./imports.json" };
import process from "bare-process";
import walletConfig from "./wallet-config.js";

async function getWalletDetails(mnemonicInit) {
  let { wallet, mnemonic } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonicInit,
    signer: new BareSparkSigner(),
    options: {
      network: "REGTEST",
    },
  });
  const balance = await wallet.getBalance();
  const sparkAddress = await wallet.getSparkAddress();
  return {
    mnemonic,
    balance,
    sparkAddress,
  };
}

const args = process.argv.slice(2);
if (args.length > 1) {
  console.error(
    "Too many arguments, please provide a mnemonic as a string, e.g. 'your mnemonic here'",
  );
  process.exit(1);
}

const config = args.length
  ? {
      mnemonic: args[0],
    }
  : walletConfig;

try {
  if (config) {
    const wDetails = await getWalletDetails(config.mnemonic);
    console.log("Initialized wallet", wDetails);
    process.exit(0);
  } else {
    const wDetails = await getWalletDetails();
    console.log("Created a new wallet", wDetails);
    process.exit(0);
  }
} catch (error) {
  console.error(error);
  process.exit(1);
}
