import { SparkWallet } from "@buildonspark/spark-sdk";

// Get mnemonic from command line arguments
const mnemonic = process.argv[2] || "your_mnemonic_here";

const wallet = new SparkWallet("REGTEST");
const wallet_mnemonic = await wallet.initWallet(mnemonic);
console.log("wallet mnemonic phrase:", wallet_mnemonic);

// Get a deposit address for Bitcoin
const depositAddress = await wallet.getDepositAddress();
console.log("Deposit Address:", depositAddress);
