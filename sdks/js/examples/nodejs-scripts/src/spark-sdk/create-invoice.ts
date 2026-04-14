import { SparkWallet } from "@buildonspark/spark-sdk";
import {
  getExampleWalletOptions,
  requireExampleMnemonic,
} from "./wallet-config.js";

// Get mnemonic and memo from command line arguments
const mnemonic = requireExampleMnemonic(process.argv[2]);
const memo = process.argv[3] || "test invoice";
const options = getExampleWalletOptions(process.env, "REGTEST");

const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
  mnemonicOrSeed: mnemonic,
  options,
});
console.log("wallet mnemonic phrase:", walletMnemonic);

// Create an invoice for 100 sats
const invoice = await wallet.createLightningInvoice({
  amountSats: 100,
  memo: memo,
});
console.log("Invoice:", invoice);
