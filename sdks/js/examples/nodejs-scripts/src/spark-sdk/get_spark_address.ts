import { SparkWallet } from "@buildonspark/spark-sdk";
import { Network } from "@buildonspark/spark-sdk/utils";

// Get mnemonic from command line arguments
const mnemonic = process.argv[2] || "your_mnemonic_here";

const wallet = new SparkWallet(Network.REGTEST);
const wallet_mnemonic = await wallet.initWallet(mnemonic);
console.log("wallet mnemonic phrase:", wallet_mnemonic);

const sparkAddress = await wallet.getSparkAddress();
console.log("Spark address:", sparkAddress);