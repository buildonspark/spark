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

// ---------------------------------------------------------------------------
// Wallet cache — keeps wallet instances alive across MCP tool calls so their
// background streams can automatically claim incoming transfers.
// ---------------------------------------------------------------------------

type CacheEntry = {
  wallet: SparkWallet;
};

/** Cache key is `mnemonic:network`. */
function cacheKey(mnemonic: string, network: string): string {
  return `${mnemonic}:${network}`;
}

const walletCache = new Map<string, CacheEntry>();
const pendingInits = new Map<string, Promise<SparkWallet>>();

/**
 * Shut down all cached wallets (aborts background streams, closes gRPC
 * connections). Call this on process exit.
 */
export async function cleanupAllWallets(): Promise<void> {
  const wallets = [...walletCache.values()].map((e) => e.wallet);
  walletCache.clear();
  pendingInits.clear();
  await Promise.allSettled(wallets.map((w) => w.cleanup()));
}

// Best-effort cleanup on process exit.
function registerExitHooks(): void {
  const handler = () => {
    cleanupAllWallets()
      .catch(() => {})
      .finally(() => process.exit(0));
  };
  process.once("SIGINT", handler);
  process.once("SIGTERM", handler);
  process.once("exit", () => {
    void cleanupAllWallets();
  });
}
registerExitHooks();

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Initialize a SparkWallet from an explicit mnemonic, falling back to the
 * SPARK_MNEMONIC environment variable. Returns a cached instance when
 * available so the wallet's background stream stays alive across calls.
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

  // When a custom initialize function is provided (tests), skip the cache.
  if (initialize) {
    const initFn = await getInitFn(initialize);
    const { wallet } = await initFn({
      mnemonicOrSeed: mnemonicToUse,
      options: { network: network as SparkNetwork },
    });
    return wallet;
  }

  const key = cacheKey(mnemonicToUse, network);

  // Return cached wallet if available.
  const cached = walletCache.get(key);
  if (cached) return cached.wallet;

  // Deduplicate concurrent init requests for the same key.
  const pending = pendingInits.get(key);
  if (pending) return pending;

  const initPromise = (async () => {
    const initFn = await getInitFn();
    const { wallet } = await initFn({
      mnemonicOrSeed: mnemonicToUse,
      options: { network: network as SparkNetwork },
    });
    walletCache.set(key, { wallet });
    pendingInits.delete(key);
    return wallet;
  })();

  pendingInits.set(key, initPromise);

  try {
    return await initPromise;
  } catch (err) {
    pendingInits.delete(key);
    throw err;
  }
}

/**
 * Generate a brand new wallet. Returns both the wallet instance and the
 * generated mnemonic — the caller is responsible for surfacing the mnemonic.
 * The wallet is added to the cache so its background stream stays alive.
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

  // Cache the freshly created wallet (skip when using injected init fn / tests).
  if (!initialize) {
    const key = cacheKey(mnemonic, network);
    walletCache.set(key, { wallet });
  }

  return { wallet, mnemonic };
}

/**
 * Remove a specific wallet from the cache, shut down its background stream,
 * and close its gRPC connections. The next resolveWallet() call for this
 * mnemonic+network will create a fresh instance.
 *
 * Returns true if a cached wallet was found and evicted, false otherwise.
 */
export async function evictWallet(
  mnemonic?: string,
  networkOverride?: BitcoinNetwork,
): Promise<boolean> {
  const config = getServerConfig();
  const network = networkOverride ?? config.defaultNetwork;
  const mnemonicToUse = mnemonic ?? process.env["SPARK_MNEMONIC"];

  if (!mnemonicToUse) {
    throw new Error(
      "No wallet specified. Pass a mnemonic parameter or set SPARK_MNEMONIC in the server env.",
    );
  }

  const key = cacheKey(mnemonicToUse, network);
  const entry = walletCache.get(key);
  if (!entry) return false;

  walletCache.delete(key);
  pendingInits.delete(key);
  await entry.wallet.cleanup();
  return true;
}

// Exported for testing only.
export function _resetCacheForTesting(): void {
  walletCache.clear();
  pendingInits.clear();
}
