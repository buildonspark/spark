import type { Transaction } from "@scure/btc-signer";
import { OutputWithPreviousTransactionData } from "../proto/spark.js";
import { TokenMetadata } from "../proto/spark_token.js";
import { ConfigOptions } from "../services/wallet-config.js";
import type { SparkSigner } from "../signer/signer.js";
import { KeyDerivation } from "../signer/types.js";
import { Bech32mTokenIdentifier } from "../utils/token-identifier.js";
import { SparkAddressFormat } from "../utils/address.js";
import { WalletTransfer } from "../types/index.js";

export type CreateLightningInvoiceParams = {
  amountSats: number;
  memo?: string;
  expirySeconds?: number;
  includeSparkAddress?: boolean;
  receiverIdentityPubkey?: string;
  descriptionHash?: string;
};

export type PayLightningInvoiceParams = {
  invoice: string;
  maxFeeSats: number;
  preferSpark?: boolean;
  amountSatsToSend?: number;
};

export type TransferParams = {
  amountSats: number;
  receiverSparkAddress: string;
};

export type TransferWithInvoiceParams = {
  amountSats: number;
  receiverIdentityPubkey: Uint8Array;
  sparkInvoice?: SparkAddressFormat;
};

export type TransferWithInvoiceOutcome =
  | { ok: true; transfer: WalletTransfer; param: TransferWithInvoiceParams }
  | { ok: false; error: Error; param: TransferWithInvoiceParams };

export type FulfillSparkInvoiceResponse = {
  satsTransactionSuccess: {
    invoice: SparkAddressFormat;
    transferResponse: WalletTransfer;
  }[];
  tokenTransactionSuccess: {
    tokenIdentifier: Bech32mTokenIdentifier;
    txid: string;
  }[];
  satsTransactionErrors: {
    invoice: string;
    error: Error;
  }[];
  tokenTransactionErrors: {
    tokenIdentifier: Bech32mTokenIdentifier;
    error: Error;
  }[];
  invalidInvoices: {
    invoice: string;
    error: Error;
  }[];
};

export type TokenInvoice = {
  invoice: string;
  identifierHex: string;
  amount: bigint;
};

export type InvalidInvoice = {
  invoice: string;
  error: Error;
};

export type GroupSparkInvoicesResult = {
  satsInvoices: TransferWithInvoiceParams[];
  tokenInvoices: Map<string, TokenInvoice[]>;
  invalidInvoices: InvalidInvoice[];
};

export type DepositParams = {
  keyDerivation: KeyDerivation;
  verifyingKey: Uint8Array;
  depositTx: Transaction;
  vout: number;
};

/**
 * Token metadata containing essential information about a token.
 * This is the wallet's internal representation with JavaScript-friendly types.
 *
 * rawTokenIdentifier: This is the raw binary token identifier - This is used to encode the bech32m encoded token identifier.
 *
 * tokenPublicKey: This is the hex-encoded public key of the token issuer - Same as issuerPublicKey.
 *
 * @example
 * ```typescript
 * const tokenMetadata: UserTokenMetadata = {
 *   rawTokenIdentifier: new Uint8Array([1, 2, 3]),
 *   tokenPublicKey: "0348fbb...",
 *   tokenName: "SparkToken",
 *   tokenTicker: "SPK",
 *   decimals: 8,
 *   maxSupply: 1000000n
 * };
 * ```
 */
export type UserTokenMetadata = {
  /** Raw binary token identifier - This is used to encode the human readable token identifier */
  rawTokenIdentifier: Uint8Array;
  /** Public key of the token issuer - Same as issuerPublicKey */
  tokenPublicKey: string;
  /** Human-readable name of the token (e.g., SparkToken)*/
  tokenName: string;
  /** Short ticker symbol for the token (e.g., "SPK") */
  tokenTicker: string;
  /** Number of decimal places for token amounts */
  decimals: number;
  /** Maximum supply of tokens that can ever be minted */
  maxSupply: bigint;
};

export type TokenBalanceMap = Map<
  Bech32mTokenIdentifier,
  {
    balance: bigint;
    tokenMetadata: UserTokenMetadata;
  }
>;

export type RawTokenIdentifierHex = string & {
  readonly __brand: "RawTokenIdentifierHex";
};

export type TokenOutputsMap = Map<
  Bech32mTokenIdentifier,
  OutputWithPreviousTransactionData[]
>;

export type TokenMetadataMap = Map<Bech32mTokenIdentifier, TokenMetadata>;

export type InitWalletResponse = {
  mnemonic?: string | undefined;
};
export interface SparkWalletProps {
  mnemonicOrSeed?: Uint8Array | string;
  accountNumber?: number;
  signer?: SparkSigner;
  options?: ConfigOptions;
}

export const SparkWalletEvent = {
  All: "*",
  TransferClaimed: "transfer:claimed",
  DepositConfirmed: "deposit:confirmed",
  StreamConnected: "stream:connected",
  StreamDisconnected: "stream:disconnected",
  StreamReconnecting: "stream:reconnecting",
} as const;

export type SparkWalletEventType =
  (typeof SparkWalletEvent)[keyof typeof SparkWalletEvent];

export interface SparkWalletEvents {
  [SparkWalletEvent.All]: (eventName: string, ...args: unknown[]) => void;
  /** Emitted when an incoming transfer is successfully claimed. Includes the transfer ID and new total balance. */
  [SparkWalletEvent.TransferClaimed]: (
    transferId: string,
    updatedBalance: bigint,
  ) => void;
  /** Emitted when a deposit is marked as available. Includes the deposit ID and new total balance. */
  [SparkWalletEvent.DepositConfirmed]: (
    depositId: string,
    updatedBalance: bigint,
  ) => void;
  /** Emitted when the stream is connected */
  [SparkWalletEvent.StreamConnected]: () => void;
  /** Emitted when the stream disconnects and fails to reconnect after max attempts */
  [SparkWalletEvent.StreamDisconnected]: (reason: string) => void;
  /** Emitted when attempting to reconnect the stream */
  [SparkWalletEvent.StreamReconnecting]: (
    attempt: number,
    maxAttempts: number,
    delayMs: number,
    error: string,
  ) => void;
}
