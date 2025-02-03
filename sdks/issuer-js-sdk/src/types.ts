import { LRCWallet } from "@wcbd/yuv-js-sdk";

export interface CreateTokenInput {
  tokenName: string;
  tokenTicker: string;
  wallet: LRCWallet;
  maxSupply: number;
  decimals: number;
  isFreezeable: boolean;
  tokenLogo?: string;
}

export interface MintTokenInput {
  tokenPublicKey: string;
  amountToMint: number;
  mintDestinationAddress: string;
  wallet: LRCWallet;
}

export interface TransferTokenInput {
  tokenPublicKey: string;
  amountToTransfer: number;
  transferDestinationAddress: string;
  wallet: LRCWallet;
}

export interface FreezeTokenInput {
  tokenPublicKey: string;
  freezeAddress: string;
  wallet: LRCWallet;
}
