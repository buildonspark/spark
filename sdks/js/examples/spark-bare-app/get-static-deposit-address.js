// Example Bare script using Spark SDK and Frost addon
import { SparkWallet } from "@buildonspark/bare";
import process from "bare-process";
import walletConfig, { getExampleWalletOptions } from "./wallet-config.js";

async function getStaticDepositAddress(mnemonicInit) {
  const options = getExampleWalletOptions(process.env, "REGTEST");
  let wallet;
  try {
    const initialized = await SparkWallet.initialize({
      mnemonicOrSeed: mnemonicInit,
      options,
    });
    wallet = initialized.wallet;
    return await wallet.getStaticDepositAddress();
  } finally {
    await wallet?.cleanup();
  }
}

const args = process.argv.slice(2);
if (args.length > 1) {
  console.error(
    "Too many arguments, please provide a mnemonic as a string, e.g. 'your mnemonic here'",
  );
  process.exit(1);
}

const config = args.length
  ? {
      mnemonic: args[0],
    }
  : walletConfig;

if (!config.mnemonic) {
  console.error(
    "No mnemonic provided in wallet-config.js or command line arguments.",
  );
  process.exit(1);
}

try {
  const staticDepositAddress = await getStaticDepositAddress(config.mnemonic);
  console.log(staticDepositAddress);
} catch (error) {
  console.error(error);
  process.exit(1);
}
