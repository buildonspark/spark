import type { SparkWallet, SparkWalletProps } from "@buildonspark/spark-sdk";
import { getServerConfig, type BitcoinNetwork } from "./config.js";

type InitializeResult = { wallet: SparkWallet; mnemonic: string | undefined };
type InitializeFn = (props: SparkWalletProps) => Promise<InitializeResult>;
type SparkNetwork = "MAINNET" | "TESTNET" | "REGTEST" | "LOCAL";

async function getInitFn(initialize?: InitializeFn): Promise<InitializeFn> {
  return (
    initialize ??
    (await import("@buildonspark/spark-sdk").then((m) =>
      // Cast is safe: .bind() removes the generic `this` parameter; the
      // return shape matches InitializeResult when T = SparkWallet.
      (m.SparkWallet.initialize as InitializeFn).bind(m.SparkWallet),
    ))
  );
}

/**
 * Initialize a SparkWallet from an explicit mnemonic, falling back to the
 * SPARK_MNEMONIC environment variable. Throws if neither is available.
 */
export async function resolveWallet(
  mnemonic?: string,
  initialize?: InitializeFn,
  networkOverride?: BitcoinNetwork,
): Promise<SparkWallet> {
  const config = getServerConfig();
  const network = networkOverride ?? config.defaultNetwork;
  const mnemonicToUse = mnemonic ?? process.env["SPARK_MNEMONIC"];

  if (!mnemonicToUse) {
    throw new Error(
      "No wallet specified. Pass a mnemonic parameter or set SPARK_MNEMONIC in the server env.",
    );
  }

  const initFn = await getInitFn(initialize);
  const { wallet } = await initFn({
    mnemonicOrSeed: mnemonicToUse,
    options: { network: network as SparkNetwork },
  });
  return wallet;
}

/**
 * Generate a brand new wallet. Returns both the wallet instance and the
 * generated mnemonic — the caller is responsible for surfacing the mnemonic.
 */
export async function createFreshWallet(
  initialize?: InitializeFn,
  networkOverride?: BitcoinNetwork,
): Promise<{ wallet: SparkWallet; mnemonic: string }> {
  const config = getServerConfig();
  const network = networkOverride ?? config.defaultNetwork;
  const initFn = await getInitFn(initialize);

  const { wallet, mnemonic } = await initFn({
    mnemonicOrSeed: undefined,
    options: { network: network as SparkNetwork },
  });

  if (!mnemonic) {
    throw new Error(
      "SDK returned no mnemonic for fresh wallet — cannot proceed.",
    );
  }

  return { wallet, mnemonic };
}
