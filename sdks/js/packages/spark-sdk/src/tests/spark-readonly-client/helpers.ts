/**
 * Shared helpers for SparkReadonlyClient integration tests.
 *
 * Uses real SparkWalletTesting instances to create funded wallets whose data
 * can then be observed through both the owner's authenticated readonly client
 * and an unauthenticated public readonly client.
 */
import { bytesToHex } from "@noble/curves/utils";
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.node.js";
import {
  SparkWalletTesting,
  SparkWalletTestingWithStream,
} from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { DefaultSparkSigner } from "../../signer/signer.js";
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
