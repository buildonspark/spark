import { SparkWallet } from "@buildonspark/bare";
import process from "bare-process";
import walletConfig, { getExampleWalletOptions } from "./wallet-config.js";

async function createLightningInvoice(mnemonicInit, amountSats) {
  const options = getExampleWalletOptions(process.env, "REGTEST");
  let wallet;
  try {
    const initialized = await SparkWallet.initialize({
      mnemonicOrSeed: mnemonicInit,
      options,
    });
    wallet = initialized.wallet;

    return await wallet.createLightningInvoice({
      amountSats: Number(amountSats),
    });
  } finally {
    await wallet?.cleanup();
  }
}

const args = process.argv.slice(2);
if (args.length !== 1) {
  console.error("Please provide exactly one argument: amount in sats.");
  process.exit(1);
}

const config = walletConfig;
if (!config.mnemonic) {
  console.error("No mnemonic provided in wallet-config.js.");
  process.exit(1);
}

try {
  const result = await createLightningInvoice(config.mnemonic, args[0]);
  console.log(result.invoice.encodedInvoice);
} catch (error) {
  console.error(error);
  process.exit(1);
}
