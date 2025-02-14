import { SparkWallet } from "spark-js-sdk/src/spark-sdk";
import { LRCWallet } from "@wcbd/yuv-js-sdk/src/index";
import { IssuerWallet, isSparkEnabled } from "./wallet";
import { mintTokensOnSpark } from "./services/spark/mint";
import { networks } from "bitcoinjs-lib";
import { NetworkType } from "@wcbd/yuv-js-sdk/src/network";
import { Network } from "spark-js-sdk/src/utils/network";
import { announceToken } from "./services/spark/create";

export interface CreateTokenInput {
  wallet: LRCWallet,
  tokenName: string;
  tokenTicker: string;
  maxSupply: bigint;
  decimals: number;
  feeRate: number;
  isFreezeable: boolean;
  tokenLogo?: string;
  network: string;
}

export interface MintTokenInput {
  wallet: IssuerWallet,
  tokenPublicKey: string;
  amountToMint: bigint;
  destinationAddress: string;
  network: string;
}

export interface TransferTokenInput {
  tokenPublicKey: string;
  amountToTransfer: bigint;
  transferDestinationAddress: string;
  network: string;
}

export interface FreezeTokenInput {
  tokenPublicKey: string;
  freezeAddress: string;
  network: string;
}

export function createLRCWallet(privateKeyHex: string): LRCWallet {
  let lrcWallet = new LRCWallet(
    privateKeyHex,
    networks.regtest,
    NetworkType.REGTEST
  );

  return lrcWallet;
}

export function createSparkWallet(): SparkWallet {
  let sparkWallet = new SparkWallet(Network.REGTEST);
  const mnemonic = sparkWallet.generateMnemonic();
  sparkWallet.createSparkWallet(mnemonic);

  return sparkWallet;
}

export function createIssuerWallet(privateKeyHex: string): IssuerWallet {
  let lrcWallet = createLRCWallet(privateKeyHex);
  let sparkWallet = createSparkWallet();

  return {
    bitcoinWallet: lrcWallet,
    sparkWallet: sparkWallet
  }
}

/**
 * Creates a new token with the specified parameters
 * returns the transaction ID of the announcement transaction
 */
export async function createToken({
  wallet,
  tokenName,
  tokenTicker,
  maxSupply,
  decimals,
  feeRate,
  isFreezeable
}: CreateTokenInput): Promise<string> {
  return await announceToken(wallet, tokenName, tokenTicker, maxSupply, decimals, isFreezeable)
}

/**
 * Mints new tokens to the specified address
 */
export async function mintTokens({
  wallet,
  tokenPublicKey,
  amountToMint,
  destinationAddress,
}: MintTokenInput) {
  if (isSparkEnabled(wallet)) {
    await mintTokensOnSpark(
      wallet.sparkWallet,
      tokenPublicKey,
      amountToMint,
    );
  }
  // do a transaction to the destination address
}

/**
 * Transfers tokens to the specified address
 */
export async function transferToken({
  tokenPublicKey,
  amountToTransfer,
  transferDestinationAddress,
}: TransferTokenInput): Promise<any> {
  throw new Error("Not implemented");
}

/**
 * Freezes tokens at the specified address
 */
export async function freezeToken({
  tokenPublicKey,
  freezeAddress,
}: FreezeTokenInput): Promise<any> {
  throw new Error("Not implemented");
}

/**
 * Gets token information by ID
 */
export async function getToken(tokenId: string): Promise<any> {
  throw new Error("Not implemented");
}