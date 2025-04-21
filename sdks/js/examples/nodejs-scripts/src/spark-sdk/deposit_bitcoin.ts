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

// Get a deposit address for Bitcoin
const depositAddress = await wallet.getSingleUseDepositAddress();
console.log("Deposit Address:", depositAddress);
