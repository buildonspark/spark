import { SparkWallet } from "@buildonspark/spark-sdk";

console.log("Spark SDK Example");

const network = "REGTEST";
const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
  options: {
    network,
  },
});

console.log("Network:", network);
