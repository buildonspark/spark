import { SparkWallet } from "@buildonspark/spark-sdk";

// Get mnemonic from command line arguments
const mnemonic = process.argv[2] || "your_mnemonic_here";

const wallet = new SparkWallet("REGTEST");
const wallet_mnemonic = await wallet.initWallet(mnemonic);
console.log("wallet mnemonic phrase:", wallet_mnemonic);

const sparkAddress = await wallet.getSparkAddress();
console.log("Spark address:", sparkAddress);
