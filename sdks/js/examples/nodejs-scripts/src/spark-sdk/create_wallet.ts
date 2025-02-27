import { SparkWallet } from "@buildonspark/spark-sdk";
import { Network } from "@buildonspark/spark-sdk/utils";

// Get optional mnemonic from command line arguments
const mnemonic = process.argv[2];  // If not provided, initWallet will generate one

const wallet = new SparkWallet(Network.REGTEST);
const wallet_mnemonic = await (mnemonic ? wallet.initWallet(mnemonic) : wallet.initWallet());
console.log("wallet mnemonic phrase:", wallet_mnemonic);