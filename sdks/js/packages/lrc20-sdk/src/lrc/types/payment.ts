import { SparkExitMetadata } from "./spark";

export interface Payment {
  recipient: string | Array<string>;
  amount: bigint;
  tokenPubkey: string;
  sats?: number;
  m?: number;
  expiryKey?: string;
  cltvOutputLocktime?: number;
  metadata?: SparkExitMetadata;
}
