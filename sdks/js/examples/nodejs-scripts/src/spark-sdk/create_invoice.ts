import { SparkWallet } from "@buildonspark/spark-sdk";

// Get mnemonic and memo from command line arguments
const mnemonic = process.argv[2] || "your_mnemonic_here";
const memo = process.argv[3] || "test invoice";

const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
  mnemonicOrSeed: mnemonic,
  options: {
    network: "REGTEST",
  },
});
console.log("wallet mnemonic phrase:", walletMnemonic);

// Create an invoice for 100 sats
const invoice = await wallet.createLightningInvoice({
  amountSats: 100,
  memo: memo,
});
console.log("Invoice:", invoice);
