//import { LRCWallet } from "lrc20-js-sdk";
import { IssuerSparkWallet } from "./services/spark/wallet.js";

export interface IssuerWallet {
  bitcoinWallet: any | undefined;
  sparkWallet: IssuerSparkWallet | undefined;
}

export const isSparkEnabled = (
  wallet: IssuerWallet
): wallet is IssuerWallet & { sparkWallet: IssuerSparkWallet } => {
  return wallet.sparkWallet !== undefined;
};

export const isLRCEnabled = (
  wallet: IssuerWallet
): wallet is IssuerWallet & { bitcoinWallet: any } => {
  // TODO: Change back to typed when lrc20-js-sdk is added.
  return wallet.bitcoinWallet !== undefined;
};
