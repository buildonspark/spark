import { SparkWallet } from "@buildonspark/spark-sdk";

// Get mnemonic from command line arguments
const mnemonic = process.argv[2] || "your_mnemonic_here";

const { wallet, mnemonic: walletMnemonic } = await SparkWallet.intialize({
  mnemonicOrSeed: mnemonic,
  options: {
    network: "REGTEST",
  },
});
console.log("wallet mnemonic:", walletMnemonic);

// Get invoice from command line arguments
const invoice = process.argv[3] || "your_invoice_here";
const invoice_response = await wallet.payLightningInvoice({ invoice });
console.log("Invoice:", invoice_response);
