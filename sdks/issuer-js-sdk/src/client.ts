import { hexToBytes } from "@noble/curves/abstract/utils";
//import { LRCWallet } from "lrc20-js-sdk";
import { IssuerWallet, isSparkEnabled } from "./wallet.js";
import { networks } from "bitcoinjs-lib";
///import { NetworkType } from "lrc20-js-sdk";
import { Network } from "@buildonspark/spark-js-sdk/utils";
import { IssuerSparkWallet } from "./services/spark/wallet.js";
//import { announceToken } from "./services/create.js";

/*
// TODO: Uncomment in follow up PR to add lrc20-js-sdk dep.
export function createLRCWallet(privateKeyHex: string): LRCWallet {
  let lrcWallet = new LRCWallet(
    privateKeyHex,
    networks.regtest,
    NetworkType.REGTEST,
  );

  return lrcWallet;
}
*/

export async function createSparkWallet(): Promise<IssuerSparkWallet> {
  let sparkWallet = new IssuerSparkWallet(Network.REGTEST);
  const mnemonic = await sparkWallet.generateMnemonic();
  await sparkWallet.createSparkWallet(mnemonic);

  return sparkWallet;
}

export async function createIssuerWallet(
  privateKey: Uint8Array
): Promise<IssuerWallet> {
  //let lrcWallet = createLRCWallet(hexToBytes.privateKey);
  let sparkWallet = await createSparkWallet();

  return {
    //bitcoinWallet: lrcWallet,
    bitcoinWallet: undefined,
    sparkWallet: sparkWallet,
  };
}

/**
 * Creates a new token with the specified parameters
 * returns the transaction ID of the announcement transaction
 */
/*
// TODO: Uncomment in follow up PR to add lrc20-js-sdk dep.
export async function createTokens(
  wallet: LRCWallet,
  tokenName: string,
  tokenTicker: string,
  maxSupply: bigint,
  decimals: number,
  isFreezeable: boolean
) {
  return await announceToken(
    wallet,
    tokenName,
    tokenTicker,
    maxSupply,
    decimals,
    isFreezeable
  );
}
*/

/**
 * Mints new tokens to the specified address
 */
export async function mintTokens(
  wallet: IssuerWallet,
  tokenPublicKey: string,
  amountToMint: bigint,
  destinationAddress: string
) {
  if (isSparkEnabled(wallet)) {
    const tokenPublicKeyBytes = hexToBytes(tokenPublicKey);

    await wallet.sparkWallet.mintTokens(tokenPublicKeyBytes, amountToMint);

    await wallet.sparkWallet.transferTokens(
      tokenPublicKeyBytes,
      amountToMint,
      hexToBytes(destinationAddress)
    );
  }
  // do a transaction to the destination address
}

/**
 * Transfers tokens to the specified address
 */
export async function transferTokens(
  wallet: IssuerWallet,
  tokenPublicKey: string,
  amountToTransfer: bigint,
  transferDestinationAddress: string
) {
  if (isSparkEnabled(wallet)) {
    const tokenPublicKeyBytes = hexToBytes(tokenPublicKey);

    await wallet.sparkWallet.transferTokens(
      tokenPublicKeyBytes,
      amountToTransfer,
      hexToBytes(transferDestinationAddress)
    );
  }
}

/**
 * Freezes tokens at the specified address
 */
export async function freezeTokens(
  wallet: IssuerWallet,
  tokenPublicKey: string,
  freezeAddress: string
) {
  if (isSparkEnabled(wallet)) {
    const tokenPublicKeyBytes = hexToBytes(tokenPublicKey);

    await wallet.sparkWallet.freezeTokens(
      tokenPublicKeyBytes,
      hexToBytes(freezeAddress)
    );
  }
}

/**
 * Gets token information by ID
 */
export async function getTokenInformation(tokenId: string): Promise<any> {
  throw new Error("Not implemented");
}
