import {
  SparkWallet,
  BareSparkSigner,
} from "@buildonspark/bare" with { imports: "./imports.json" };
import process from "bare-process";
import walletConfig from "./wallet-config.js";

async function createLightningInvoice(mnemonicInit, amountSats) {
  const { wallet } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonicInit,
    signer: new BareSparkSigner(),
    options: {
      network: "REGTEST",
    },
  });

  const invoice = await wallet.createLightningInvoice({
    amountSats: Number(amountSats),
  });
  await wallet.cleanupConnections();
  return invoice;
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
