import { LRCWallet } from "lrc20-js-sdk";
import { IssuerSparkWallet } from "./services/spark/wallet.js";

export interface IssuerWallet {
  bitcoinWallet: LRCWallet | undefined,
  sparkWallet: IssuerSparkWallet | undefined,
}

export const isSparkEnabled = (wallet: IssuerWallet): wallet is IssuerWallet & { sparkWallet: IssuerSparkWallet } => {
  return wallet.sparkWallet !== undefined;
};

export const isLRCEnabled = (wallet: IssuerWallet): wallet is IssuerWallet & { bitcoinWallet: LRCWallet } => {
  return wallet.bitcoinWallet !== undefined;
};