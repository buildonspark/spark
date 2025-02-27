import { SparkWallet } from "@buildonspark/spark-sdk";
import { Network } from "@buildonspark/spark-sdk/utils";

// Get mnemonic and memo from command line arguments
const mnemonic = process.argv[2] || "your_mnemonic_here";
const memo = process.argv[3] || "test invoice";

const wallet = new SparkWallet(Network.REGTEST);
const wallet_mnemonic = await wallet.initWallet(mnemonic);
console.log("wallet mnemonic phrase:", wallet_mnemonic);

// Create an invoice for 100 sats
const invoice = await wallet.createLightningInvoice({
    amountSats: 100,
    memo: memo,
});
console.log("Invoice:", invoice);