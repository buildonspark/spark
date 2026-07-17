/**
 * Shared helpers for SparkReadonlyClient integration tests.
 *
 * Uses real SparkWalletTesting instances to create funded wallets whose data
 * can then be observed through both the owner's authenticated readonly client
 * and an unauthenticated public readonly client.
 */
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.node.js";
import {
  SparkWalletTesting,
  SparkWalletTestingWithStream,
} from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { retryUntilSuccess } from "../utils/utils.js";
import { type DefaultSparkSigner } from "../../signer/signer.js";
import type { ConfigOptions } from "../../services/wallet-config.js";
import { encodeSparkAddress } from "../../utils/address.js";

/** Default options used across all readonly-client integration tests. */
export const LOCAL_OPTIONS: ConfigOptions = { network: "LOCAL" };

/** Static mnemonic so readonly clients can consistently derive the same identity. */
export const TEST_MNEMONIC =
  "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";

// ── Wallet Setup ────────────────────────────────────────────────

export interface FundedWallet {
  /** The full SparkWallet instance (for performing deposits, transfers, etc.). */
  wallet: SparkWalletTesting;
  /** The wallet's spark address string. */
  sparkAddress: string;
  /** The wallet's identity public key as hex. */
  identityPublicKey: string;
  /** The mnemonic used to create this wallet. */
  mnemonic: string;
}

/**
 * Creates a new wallet, funds it with the given amount using a faucet deposit,
 * and returns all the pieces needed for subsequent readonly queries.
 */
export async function createFundedWallet(
  amountSats: bigint = 10_000n,
): Promise<FundedWallet> {
  const faucet = BitcoinFaucet.getInstance();
  const { wallet, mnemonic } = await SparkWalletTestingWithStream.initialize({
    options: LOCAL_OPTIONS,
  });

  const depositAddress = await wallet.getSingleUseDepositAddress();
  const signedTx = await faucet.sendToAddress(depositAddress, amountSats);
  await faucet.mineBlocksAndWaitForMiningToComplete(3);
  await wallet.claimDeposit(signedTx.id);
  await retryUntilSuccess(
    async () => {
      const balance = await wallet.getBalance();
      if (balance.satsBalance.available !== amountSats) {
        throw new Error(
          `expected available balance ${amountSats}, got ${balance.satsBalance.available}`,
        );
      }
    },
    { maxAttempts: 20, delayMs: 1000 },
  );

  const sparkAddress = await wallet.getSparkAddress();
  const identityPublicKey = await wallet.getIdentityPublicKey();

  return {
    wallet,
    sparkAddress,
    identityPublicKey,
    mnemonic: mnemonic!,
  };
}

/**
 * Creates a new (unfunded) wallet and returns its info.
 * Useful for testing empty-state queries.
 */
export async function createEmptyWallet(): Promise<FundedWallet> {
  const { wallet, mnemonic } = await SparkWalletTesting.initialize({
    options: LOCAL_OPTIONS,
  });

  const sparkAddress = await wallet.getSparkAddress();
  const identityPublicKey = await wallet.getIdentityPublicKey();

  return {
    wallet,
    sparkAddress,
    identityPublicKey,
    mnemonic: mnemonic!,
  };
}

// ── Readonly Client Factories ───────────────────────────────────

/**
 * Creates a public (unauthenticated) readonly client.
 * This is how a third party would query data for any wallet.
 */
export function createPublicReadonlyClient(): SparkReadonlyClient {
  return SparkReadonlyClient.createPublic(LOCAL_OPTIONS);
}

/**
 * Creates a readonly client authenticated as the owner of the given mnemonic.
 * This is how the wallet owner can query their own data (even if privacy is enabled).
 */
export async function createOwnerReadonlyClient(
  mnemonic: string,
): Promise<SparkReadonlyClient> {
  await Promise.resolve();
  return SparkReadonlyClient.createWithMasterKey(LOCAL_OPTIONS, mnemonic);
}

/**
 * Creates a readonly client with a specific signer already initialized.
 */
export function createSignerReadonlyClient(
  signer: DefaultSparkSigner,
): SparkReadonlyClient {
  return SparkReadonlyClient.createWithSigner(LOCAL_OPTIONS, signer);
}

// ── Privacy Convergence ─────────────────────────────────────────

/** Rejects if `promise` doesn't settle within `ms`, so a stalled SO can't hang the poll. */
async function withReadTimeout<T>(
  promise: Promise<T>,
  ms: number,
  label: string,
): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_, reject) => {
        timer = setTimeout(
          () => reject(new Error(`${label} exceeded ${ms}ms`)),
          ms,
        );
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/**
 * Waits until the public client observes a privatized wallet as empty, so
 * enforcement assertions don't race gossip propagation of the wallet setting to
 * peer SOs. Requires consecutive zero reads so one already-gossiped operator
 * can't satisfy the wait while a peer still serves the wallet as public. Each
 * read is deadline-bounded and the whole wait is time-boxed — the readonly-client
 * RPCs carry no gRPC deadline, so an unbounded read against a stalled SO would
 * otherwise hang to the Jest hook timeout. Throws its own error on timeout, so a
 * genuine enforcement regression fails rather than hangs.
 */
export async function waitForPrivacyConvergence(
  publicClient: SparkReadonlyClient,
  sparkAddress: string,
): Promise<void> {
  const convergenceTimeoutMs = 60_000;
  const perReadTimeoutMs = 5_000;
  const requiredConsecutiveZeroReads = 5;
  const pollIntervalMs = 500;

  const deadline = Date.now() + convergenceTimeoutMs;
  let consecutiveZeroReads = 0;
  let lastObserved = "no successful read";

  while (Date.now() < deadline) {
    try {
      const [available, owned] = await Promise.all([
        withReadTimeout(
          publicClient.getAvailableBalance(sparkAddress),
          perReadTimeoutMs,
          "getAvailableBalance",
        ),
        withReadTimeout(
          publicClient.getOwnedBalance(sparkAddress),
          perReadTimeoutMs,
          "getOwnedBalance",
        ),
      ]);
      if (available === 0n && owned === 0n) {
        if (++consecutiveZeroReads >= requiredConsecutiveZeroReads) {
          return;
        }
        continue;
      }
      consecutiveZeroReads = 0;
      lastObserved = `available=${available}, owned=${owned}`;
    } catch (err) {
      consecutiveZeroReads = 0;
      lastObserved = err instanceof Error ? err.message : String(err);
    }
    await new Promise((r) => setTimeout(r, pollIntervalMs));
  }

  throw new Error(
    `privacy enforcement did not converge within ${convergenceTimeoutMs}ms (last: ${lastObserved})`,
  );
}

// ── Address Helpers ─────────────────────────────────────────────

/**
 * Encodes a spark address from a hex identity public key.
 */
export function sparkAddressFromPubkey(identityPublicKeyHex: string): string {
  return encodeSparkAddress({
    identityPublicKey: identityPublicKeyHex,
    network: "LOCAL",
  });
}
