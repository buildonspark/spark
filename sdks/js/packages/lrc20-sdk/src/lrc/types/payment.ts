import { SparkExitMetadata } from "./spark.ts";

export interface Payment {
  recipient?: string | Array<string>;
  amount: bigint;
  tokenPubkey: string;
  sats?: number;
  m?: number;
  expiryKey?: string;
  revocationKey?: string;
  cltvOutputLocktime?: number;
  metadata?: SparkExitMetadata;
}
