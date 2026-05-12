// Example Bare script using Spark SDK and Frost addon
import { SparkWallet } from "@buildonspark/bare";
import process from "bare-process";
import walletConfig, { getExampleWalletOptions } from "./wallet-config.js";

async function getOrCreateWallet(mnemonicInit) {
  const options = getExampleWalletOptions(process.env, "REGTEST");
  const { wallet, mnemonic } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonicInit,
    options,
  });
  const balance = await wallet.getBalance();
  const sparkAddress = await wallet.getSparkAddress();
  await wallet.cleanup();
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
  if (config.mnemonic) {
    const wDetails = await getOrCreateWallet(config.mnemonic);
    console.log("Initialized wallet", wDetails);
  } else {
    const wDetails = await getOrCreateWallet();
    console.log("Created a new wallet", wDetails);
  }
} catch (error) {
  console.error(error);
  process.exit(1);
}
