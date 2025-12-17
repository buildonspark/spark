import type { Bech32mTokenIdentifier } from "@buildonspark/spark-sdk";

/**
 * Token metadata containing essential information about issuer's token.
 * This is the wallet's internal representation with JavaScript-friendly types.
 *
 * rawTokenIdentifier: This is the raw binary token identifier - This is used to encode the human readable token identifier.
 *
 * tokenPublicKey: This is the hex-encoded public key of the token issuer - Same as issuerPublicKey.
 *
 * @example
 * ```typescript
 * const tokenMetadata: IssuerTokenMetadata = {
 *   rawTokenIdentifier: new Uint8Array([1, 2, 3]),
 *   tokenPublicKey: "0348fbb...",
 *   tokenName: "SparkToken",
 *   tokenTicker: "SPK",
 *   decimals: 8,
 *   maxSupply: 1000000n
 *   isFreezable: true
 * };
 * ```
 */
export type IssuerTokenMetadata = {
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
  /** Whether the token is freezable */
  isFreezable: boolean;
  /** Extra metadata of the token */
  extraMetadata?: Uint8Array;
  /** Bech32m encoded token identifier */
  bech32mTokenIdentifier: Bech32mTokenIdentifier;
};

export interface TokenDistribution {
  totalCirculatingSupply: bigint;
  totalIssued: bigint;
  totalBurned: bigint;
  numHoldingAddress: number;
  numConfirmedTransactions: bigint;
}

/**
 * Details of a token creation.
 *
 * tokenIdentifier: The Bech32m encoded token identifier.
 * transactionHash: The hash of the transaction that created the token.
 *
 * @example
 * ```typescript
 * const tokenCreationDetails: TokenCreationDetails = {
 *   tokenIdentifier: "btkn1...",
 *   transactionHash: "1234567890abcdef...",
 * };
 * ```
 */
export interface TokenCreationDetails {
  /** Bech32m encoded token identifier */
  tokenIdentifier: Bech32mTokenIdentifier;
  /** Transaction hash of the announcement */
  transactionHash: string;
}
