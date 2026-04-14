import { SparkWallet } from "@buildonspark/spark-sdk";
import {
  getExampleWalletOptions,
  requireExampleMnemonic,
} from "./wallet-config.js";

async function main() {
  // Get mnemonic and receiver address from command line arguments
  const mnemonic = requireExampleMnemonic(process.argv[2]);
  const receiverAddress = process.argv[3] || "your_receiver_address_here";
  const options = getExampleWalletOptions(process.env, "REGTEST");

  // Initialize wallet with configuration object
  const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonic,
    options,
  });

  console.log("wallet mnemonic phrase:", walletMnemonic);

  const balance = await wallet.getBalance();
  console.log("Balance:", balance);

  const transfer = await wallet.transfer({
    receiverSparkAddress: receiverAddress,
    amountSats: 100,
  });
  console.log("Transfer:", transfer);

  const new_balance = await wallet.getBalance();
  console.log("New Balance:", new_balance);
}

main().catch((error) => {
  console.error("Error:", error);
});
