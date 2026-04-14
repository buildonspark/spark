import { SparkWallet } from "@buildonspark/spark-sdk";
import {
  getExampleMnemonic,
  getExampleWalletOptions,
} from "./wallet-config.js";

async function getOrCreateWallet(mnemonicInit?: string) {
  const options = getExampleWalletOptions(process.env, "REGTEST");
  const { wallet, mnemonic } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonicInit,
    options,
  });
  const balance = await wallet.getBalance();
  const sparkAddress = await wallet.getSparkAddress();
  await wallet.cleanupConnections();
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

const mnemonic = getExampleMnemonic(args[0]);

try {
  if (mnemonic) {
    console.log("Initialized wallet", await getOrCreateWallet(mnemonic));
  } else {
    console.log("Created a new wallet", await getOrCreateWallet());
  }
} catch (error) {
  console.error(error);
  process.exit(1);
}
