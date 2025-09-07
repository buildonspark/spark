// Example Bare script using Spark SDK and Frost addon
import {
  SparkWallet,
  BareSparkSigner,
} from "@buildonspark/bare" with { imports: "./imports.json" };
import process from "bare-process";
import walletConfig from "./wallet-config.js";

async function transfer(mnemonicInit, receiverSparkAddress, amountSats) {
  let { wallet, mnemonic } = await SparkWallet.initialize({
    signer: new BareSparkSigner(),
    mnemonicOrSeed: mnemonicInit,
    options: {
      network: "REGTEST",
    },
  });
  const transfer = await wallet.transfer({
    receiverSparkAddress,
    amountSats: Number(amountSats),
  });
  return transfer;
}

const args = process.argv.slice(2);
if (args.length !== 2) {
  console.error("Please provide receiver Spark address and amount in sats");
  process.exit(1);
}

const config = walletConfig;

if (!config.mnemonic) {
  console.error("No mnemonic provided in wallet-config.js.");
  process.exit(1);
}

const receiverSparkAddress = args[0];
if (!receiverSparkAddress) {
  console.error("No receiver Spark address provided.");
  process.exit(1);
}

const amountSats = args[1];
if (!amountSats) {
  console.error("No amount in sats provided.");
  process.exit(1);
}

console.log();

try {
  const transferResult = await transfer(
    config.mnemonic,
    receiverSparkAddress,
    amountSats,
  );
  console.log("Transfer result:", transferResult);
  process.exit(0);
} catch (error) {
  console.error(error);
  process.exit(1);
}
