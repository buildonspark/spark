// Example Bare script using Spark SDK and Frost addon
import {
  SparkWallet,
  BareSparkSigner,
} from "@buildonspark/bare" with { imports: "./imports.json" };
import process from "bare-process";
import walletConfig from "./wallet-config.js";

async function getStaticDepositAddress(mnemonicInit) {
  let { wallet, mnemonic } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonicInit,
    signer: new BareSparkSigner(),
    options: {
      network: "REGTEST",
    },
  });
  const staticDepositAddress = await wallet.getStaticDepositAddress();
  return staticDepositAddress;
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
  process.exit(0);
} catch (error) {
  console.error(error);
  process.exit(1);
}
