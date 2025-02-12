import { SparkWallet } from "../../dist/spark-sdk";
import { Network } from "../../dist/utils/network";

// Initialize Spark Wallet
const mnemonic =
  "typical stereo dose party penalty decline neglect feel harvest abstract stage winter";
const wallet = new SparkWallet(Network.REGTEST);
// alternatively generate new mnemonic
// const mnemonic = wallet.generateMnemonic();

const pubKey = await wallet.createSparkWallet(mnemonic);

// Test

// const invoice = await wallet.createLightningInvoice({
//   amountSats: 1000,
//   memo: "Test Invoice",
//   expirySeconds: 60 * 60 * 24,
// });

// console.log(invoice);

const pendingTransfers = await wallet.queryPendingTransfers();

for (const transfer of pendingTransfers.transfers) {
  console.log(transfer);
  await wallet.claimTransfer(transfer);
}
