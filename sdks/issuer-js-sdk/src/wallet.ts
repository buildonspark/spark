import { SparkWallet } from "spark-js-sdk/src/spark-sdk";
import { LRCWallet } from "@wcbd/yuv-js-sdk/src/index";

export interface IssuerWallet {
  bitcoinWallet: LRCWallet | undefined,
  sparkWallet: SparkWallet | undefined,
}

export const isSparkEnabled = (wallet: IssuerWallet): wallet is IssuerWallet & { sparkWallet: SparkWallet } => {
  return wallet.sparkWallet !== undefined;
};

export const isLRCEnabled = (wallet: IssuerWallet): wallet is IssuerWallet & { bitcoinWallet: LRCWallet } => {
  return wallet.bitcoinWallet !== undefined;
};