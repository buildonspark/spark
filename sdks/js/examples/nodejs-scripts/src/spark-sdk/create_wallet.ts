import { SparkWallet } from "@buildonspark/spark-sdk";

// Get optional mnemonic from command line arguments
const mnemonic = process.argv[2]; // If not provided, initWallet will generate one

const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
  mnemonicOrSeed: mnemonic,
  options: {
    network: "REGTEST",
  },
});
console.log("wallet mnemonic phrase:", walletMnemonic);
