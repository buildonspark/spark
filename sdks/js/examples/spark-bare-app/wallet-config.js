/* Add your mnemonic phrase here for use with the scripts in spark-bare-app, or leave undefined to
   initialize a new wallet. Note seed phrase is required here for some of the test scripts */

import {
  mergeConfigOptionsForNetwork,
  normalizeNetworkType,
} from "@buildonspark/bare";

export function getExampleSparkNetwork(
  env = process.env,
  defaultNetwork = "REGTEST",
) {
  return normalizeNetworkType(
    env.NETWORK ?? env.SPARK_NETWORK ?? getConfigOverride(env)?.network,
    defaultNetwork,
  );
}

export function getExampleWalletOptions(
  env = process.env,
  defaultNetwork = "REGTEST",
) {
  return mergeConfigOptionsForNetwork(
    getExampleSparkNetwork(env, defaultNetwork),
    getConfigOverride(env),
  );
}

function getConfigOverride(env) {
  if (
    typeof env.SPARK_CONFIG_JSON !== "string" ||
    env.SPARK_CONFIG_JSON === ""
  ) {
    return undefined;
  }

  return JSON.parse(env.SPARK_CONFIG_JSON);
}

const walletConfig = {
  mnemonic: undefined,
};

export default walletConfig;
