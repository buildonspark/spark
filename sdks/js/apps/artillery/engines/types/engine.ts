import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import type { EngineStep } from "./steps";

export interface EngineAction {
  (params?: any): EngineStep;
}

export interface WalletInfo {
  wallet: IssuerSparkWallet;
  address: string;
  publicKey: string;
  balance: bigint;
  mnemonic?: string;
}

export interface WalletPool {
  name: string;
  size: number;
  wallets: IssuerSparkWallet[];
  available: IssuerSparkWallet[];
  locked: Set<IssuerSparkWallet>;
}
