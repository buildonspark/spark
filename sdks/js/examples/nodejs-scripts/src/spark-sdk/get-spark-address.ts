import { SparkWallet } from "@buildonspark/spark-sdk";
import {
  getExampleWalletOptions,
  requireExampleMnemonic,
} from "./wallet-config.js";

// Get mnemonic from command line arguments
const mnemonic = requireExampleMnemonic(process.argv[2]);
const options = getExampleWalletOptions(process.env, "REGTEST");

const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
  mnemonicOrSeed: mnemonic,
  options,
});
console.log("wallet mnemonic phrase:", walletMnemonic);

const sparkAddress = await wallet.getSparkAddress();
console.log("Spark address:", sparkAddress);
