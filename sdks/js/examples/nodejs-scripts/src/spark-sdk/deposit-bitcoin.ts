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

console.log("wallet mnemonic phrase:", walletMnemonic);

// Get a deposit address for Bitcoin
const depositAddress = await wallet.getSingleUseDepositAddress();
console.log("Deposit Address:", depositAddress);
