import { BitcoinNetwork } from "../../dist/graphql/objects";
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

// const pendingTransfers = await wallet.queryPendingTransfers();

// for (const transfer of pendingTransfers.transfers) {
//   console.log(transfer);
//   await wallet.claimTransfer(transfer);
// }

console.log(await wallet.getBalance());
console.log(
  await wallet.getLightningReceiveFeeEstimate({
    amountSats: 1000,
    network: BitcoinNetwork.REGTEST,
  })
);

console.log(
  await wallet.getLightningSendFeeEstimate({
    encodedInvoice:
      "lnbcrt10u1pn6e7wspp5036432kce4y2rfgpv0r9tckxdywnwra93yvnmk3wwdqrwznpscsssp5pg5w4asz2ahn3lxcquc2jly530l28l2tck30uh27tyscv9j7ut0qxqyz5vqnp4qfz4e5dusdsywfz775rer39sqnplylch25akk3ls0x5t2xj8avugcrzjqwqhx2af8m8h5sw9t4larcfh3vzcyyjjkl6j95mzuge7r47zqa7tkqqqqr02azsxkgqqqqqqqqqqqqqq9qcqzpgdq523jhxapqf9h8vmmfvdjs9qyyssqmhyfj8990pd9ce87n02cr72kyamt6jkl9855xz3p2rl86daa30p5n2nmedcsgmzsyg4e40p7ezvd6mkh45drw6e4zs373w5jjcr5u2qqw07n2a",
  })
);
