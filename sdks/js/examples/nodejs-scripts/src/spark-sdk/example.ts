import { SparkWallet } from "@buildonspark/spark-sdk";
import {
  getExampleSparkNetwork,
  getExampleWalletOptions,
} from "./wallet-config.js";

console.log("Spark SDK Example");

const network = getExampleSparkNetwork(process.env, "REGTEST");
const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
  options: getExampleWalletOptions(process.env, network),
});

console.log("Network:", network);
