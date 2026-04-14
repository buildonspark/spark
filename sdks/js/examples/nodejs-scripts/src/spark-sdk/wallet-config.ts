import fs from "node:fs";
import path from "node:path";
import {
  mergeConfigOptionsForNetwork,
  normalizeNetworkType,
  type ConfigOptions,
  type NetworkType,
} from "@buildonspark/spark-sdk";

/* Add your mnemonic phrase here for use with the scripts in nodejs-scripts, or leave undefined to
   initialize a new wallet. */

const walletConfig = {
  mnemonic: undefined as string | undefined,
};

export default walletConfig;

export function getExampleSparkNetwork(
  env: NodeJS.ProcessEnv,
  defaultNetwork: NetworkType = "REGTEST",
): NetworkType {
  return normalizeNetworkType(
    env["NETWORK"] ?? env["SPARK_NETWORK"] ?? getConfigOverride(env)?.network,
    defaultNetwork,
  );
}

export function getExampleWalletOptions(
  env: NodeJS.ProcessEnv,
  defaultNetwork: NetworkType = "REGTEST",
): ConfigOptions {
  return mergeConfigOptionsForNetwork(
    getExampleSparkNetwork(env, defaultNetwork),
    getConfigOverride(env),
  );
}

export function getExampleMnemonic(mnemonicArg?: string): string | undefined {
  return mnemonicArg ?? walletConfig.mnemonic;
}

export function requireExampleMnemonic(mnemonicArg?: string): string {
  const mnemonic = getExampleMnemonic(mnemonicArg);
  if (!mnemonic) {
    throw new Error(
      "No mnemonic provided in wallet-config.ts or command line arguments.",
    );
  }

  return mnemonic;
}

function getConfigOverride(env: NodeJS.ProcessEnv): ConfigOptions | undefined {
  const configFile = env["CONFIG_FILE"];
  if (!configFile) {
    return undefined;
  }

  return JSON.parse(
    fs.readFileSync(path.resolve(process.cwd(), configFile), "utf8"),
  ) as ConfigOptions;
}
