import { SparkWallet } from "@buildonspark/spark-sdk";
import {
  getExampleWalletOptions,
  requireExampleMnemonic,
} from "./wallet-config.js";

// Get mnemonic from command line arguments
const mnemonic = requireExampleMnemonic(process.argv[2]);
const options = getExampleWalletOptions(process.env, "REGTEST");

const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
  mnemonicOrSeed: mnemonic,
  options,
});
console.log("wallet mnemonic:", walletMnemonic);

// Get invoice from command line arguments
const invoice = process.argv[3] || "your_invoice_here";
const maxFeeSats = process.argv[4] || "0";
const invoice_response = await wallet.payLightningInvoice({
  invoice,
  maxFeeSats: parseInt(maxFeeSats),
});
console.log("Invoice:", invoice_response);
