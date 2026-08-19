import type { Transaction } from "@scure/btc-signer";
import { type TransferManifest } from "../proto/spark.js";
import {
  type OutputWithPreviousTransactionData,
  type TokenMetadata,
} from "../proto/spark_token.js";
import { type ReceiveQuoteAmountBasis } from "../utils/receive-quote.js";
import { type ConfigOptions } from "../services/wallet-config.js";
import type { SparkSigner } from "../signer/signer.js";
import { type KeyDerivation } from "../signer/types.js";
import {
  type CoopExitFeeQuote,
  type ExitSpeed,
  type WalletTransfer,
} from "../types/index.js";
import { type SparkAddressFormat } from "../utils/address.js";
import { type Bech32mTokenIdentifier } from "../utils/token-identifier.js";
import type { UUID } from "../utils/transfer-id.js";
import type { SparkWallet } from "./spark-wallet.js";

export type WithdrawParams = {
  onchainAddress: string;
  exitSpeed: ExitSpeed;
  /**
   * @deprecated Use feeQuoteId and feeAmountSats instead
   */
  feeQuote?: CoopExitFeeQuote;
  amountSats?: number;
  feeQuoteId?: string;
  feeAmountSats?: number;
  deductFeeFromWithdrawalAmount?: boolean;
};

export type GetLightningReceiveQuoteParams = {
  amountSats: number;
  /**
   * Defaults to NET. GROSS requires an SSP schema exposing `amount_basis`;
   * against an older one the request is refused rather than quietly quoted as
   * NET, which would invoice for more than was asked for.
   */
  amountBasis?: ReceiveQuoteAmountBasis;
  /**
   * Partner JWT to attribute the quote to. Without one the SSP quotes feeless,
   * reporting why in `attributionStatus`; a rejected token is never an error.
   */
  partnerJwt?: string;
};

/**
 * A quote issued by the SSP, carried verbatim into the invoice request.
 *
 * `serializedManifest` and `issuerSignature` are echoed exactly as received —
 * re-encoding a decoded copy is not guaranteed to reproduce them. The amount
 * and basis travel along so the checks run against what was actually asked
 * for rather than whatever a caller passes second.
 */
export type LightningReceiveQuote = {
  serializedManifest: string;
  issuerSignature: string;
  /** Absent on a quote rebuilt from its wire values; advisory either way. */
  attributionStatus?: string;
  manifest: TransferManifest;
  amountSats: number;
  amountBasis: ReceiveQuoteAmountBasis;
};

export type CreateLightningInvoiceParams = {
  amountSats: number;
  memo?: string;
  expirySeconds?: number;
  includeSparkAddress?: boolean;
  includeSparkInvoice?: boolean;
  receiverIdentityPubkey?: string;
  descriptionHash?: string;
  /** A quote to sign and send with this invoice. Omit for an unquoted invoice. */
  quote?: LightningReceiveQuote;
};

export type CreateLightningHodlInvoiceParams = {
  amountSats: number;
  paymentHash: string;
  memo?: string;
  expirySeconds?: number;
  includeSparkAddress?: boolean;
  includeSparkInvoice?: boolean;
  receiverIdentityPubkey?: string;
  descriptionHash?: string;
};

export type PayLightningInvoiceParams = {
  invoice: string;
  maxFeeSats: number;
  preferSpark?: boolean;
  amountSatsToSend?: number;
  transferId?: UUID;
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
    invoices: SparkAddressFormat[];
    txid: string;
  }[];
  satsTransactionErrors: {
    invoice: SparkAddressFormat;
    error: Error;
  }[];
  tokenTransactionErrors: {
    tokenIdentifier: Bech32mTokenIdentifier;
    invoices: SparkAddressFormat[];
    error: Error;
  }[];
  invalidInvoices: InvalidInvoice[];
};

export type TokenInvoice = {
  invoice: SparkAddressFormat;
  identifierHex: string;
  amount: bigint;
};

export type InvalidInvoice = {
  invoice: SparkAddressFormat;
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
 * rawTokenIdentifier: This is the raw binary token identifier - This is used to encode the bech32m
 * encoded token identifier.
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
  /** Extra metadata of the token */
  extraMetadata?: Uint8Array;
};

export type TokenBalanceMap = Map<
  Bech32mTokenIdentifier,
  {
    ownedBalance: bigint;
    availableToSendBalance: bigint;
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

export type { TokenOutputLock } from "../services/tokens/output-manager.js";

export type TokenMetadataMap = Map<Bech32mTokenIdentifier, TokenMetadata>;

export type InitWalletResponse<T extends SparkWallet = SparkWallet> = {
  wallet: T;
  mnemonic: string | undefined;
};
export interface SparkWalletProps {
  mnemonicOrSeed?: Uint8Array | string;
  accountNumber?: number;
  signer?: SparkSigner;
  options?: ConfigOptions;
}

export type HandlePublicMethodErrorParams = {
  serverTraceId?: string;
  wallet?: SparkWallet;
};

export const SparkWalletEvent = {
  All: "*",
  BalanceUpdate: "balance:update",
  TokenBalanceUpdate: "token-balance:update",
  TransferClaimed: "transfer:claimed",
  DepositConfirmed: "deposit:confirmed",
  StreamConnected: "stream:connected",
  StreamDisconnected: "stream:disconnected",
  StreamReconnecting: "stream:reconnecting",
} as const;

export const SPARK_WALLET_CLEANUP_DISCONNECT_REASON =
  "Wallet cleanup requested";

export type SparkWalletEventType =
  (typeof SparkWalletEvent)[keyof typeof SparkWalletEvent];

export interface SparkWalletEvents {
  [SparkWalletEvent.All]: (eventName: string, ...args: unknown[]) => void;
  /** Emitted whenever the balance changes (deposits, transfers, swaps, claims). */
  [SparkWalletEvent.BalanceUpdate]: (balance: {
    available: bigint;
    owned: bigint;
    incoming: bigint;
  }) => void;
  /**
   * Emitted when a token transaction is finalized (both sender and receiver). Includes the updated
   * balances for affected tokens.
   */
  [SparkWalletEvent.TokenBalanceUpdate]: (event: {
    finalizedTokenTransactions: Array<{
      tokenTransactionHash: Uint8Array;
      tokenIdentifiers: string[];
      sparkInvoices: string[];
    }>;
    tokenBalances: TokenBalanceMap;
  }) => void;
  /**
   * Emitted when an incoming transfer is successfully claimed. Includes the transfer ID and new
   * total balance.
   */
  [SparkWalletEvent.TransferClaimed]: (
    transferId: string,
    updatedBalance: bigint,
  ) => void;
  /**
   * Emitted when a deposit is marked as available. Includes the deposit ID and new total balance.
   */
  [SparkWalletEvent.DepositConfirmed]: (
    depositId: string,
    updatedBalance: bigint,
  ) => void;
  /** Emitted when the stream is connected */
  [SparkWalletEvent.StreamConnected]: () => void;
  /** Emitted when the stream stops and will not retry again on this wallet instance. */
  [SparkWalletEvent.StreamDisconnected]: (reason: string) => void;
  /**
   * Emitted when attempting to reconnect the stream. maxAttempts is Infinity when retries are
   * unbounded.
   */
  [SparkWalletEvent.StreamReconnecting]: (
    attempt: number,
    maxAttempts: number,
    delayMs: number,
    error: string,
  ) => void;
}

export type TransferReceiver = {
  receiverSparkAddress: string;
  amountSats: number;
};

export type TransferV2Params = {
  receivers: TransferReceiver[];
};

export type CreateHTLCParams = {
  receiverSparkAddress: string;
  amountSats: number;
  preimage?: string;
  expiryTime: Date;
};

/**
 * A leaf whose exit path a watchtower exit cut off, as returned by
 * SparkWallet.getWatchtowerExitedLeaves.
 */
export type WatchtowerExitedLeaf = {
  leafId: string;
  status: "WATCHTOWER_EXITED" | "WATCHTOWER_EXIT_RECOVERED";
  /** The leaf's recorded value. */
  valueSats: number;
  /**
   * The on-chain output to spend, absent when none could be resolved. Its
   * valueSats is what recovery actually pays out from, before the fee — the
   * output predates the leaf's last renewals, so it can differ from the leaf's.
   */
  recovery?: {
    txid: string;
    outputIndex: number;
    valueSats: number;
  };
};
