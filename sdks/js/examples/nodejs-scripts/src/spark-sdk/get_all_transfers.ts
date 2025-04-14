import { SparkWallet } from "@buildonspark/spark-sdk";

// Get mnemonic from command line arguments
const mnemonic = process.argv[2] || "your_mnemonic_here";

const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
  mnemonicOrSeed: mnemonic,
  options: {
    network: "REGTEST",
  },
});
console.log("wallet mnemonic phrase:", walletMnemonic);

const transfers = await wallet.getTransfers(20, 0);
console.log("Transfers:", transfers);
