import type { IssuerSparkWallet } from "@buildonspark/issuer-sdk";

export interface SparkContext {
  sparkWallet?: IssuerSparkWallet;
  senderWallet?: IssuerSparkWallet;
  receiverWallet?: IssuerSparkWallet;
  vars?: Record<string, any>;
  _scenarioSpec?: any;
  _artillery?: any;
  scenario?: any;
  scenarioLockedWallets?: string[];
  _uid?: string;
  _preLocked?: Record<string, boolean>;
}

export type StepResult = {
  error?: Error;
  context?: SparkContext;
};

export type StepCallback = (error?: Error, context?: SparkContext) => void;
