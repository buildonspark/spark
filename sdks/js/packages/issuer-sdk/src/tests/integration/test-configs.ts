import { ConfigOptions, WalletConfig } from "@buildonspark/spark-sdk";

const SHORTER_TOKEN_OUTPUT_LOCK_EXPIRY_MS = 5000;

export const TOKENS_SCHNORR_V2_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "SCHNORR",
  tokenOutputLockExpiryMs: SHORTER_TOKEN_OUTPUT_LOCK_EXPIRY_MS,
  tokenTransactionVersion: "V2",
};

export const TOKENS_SCHNORR_V3_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "SCHNORR",
  tokenOutputLockExpiryMs: SHORTER_TOKEN_OUTPUT_LOCK_EXPIRY_MS,
  tokenTransactionVersion: "V3",
};

export const TOKENS_ECDSA_V2_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "ECDSA",
  tokenOutputLockExpiryMs: SHORTER_TOKEN_OUTPUT_LOCK_EXPIRY_MS,
  tokenTransactionVersion: "V2",
};

export const TOKENS_ECDSA_V3_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "ECDSA",
  tokenOutputLockExpiryMs: SHORTER_TOKEN_OUTPUT_LOCK_EXPIRY_MS,
  tokenTransactionVersion: "V3",
};

export const TEST_CONFIGS = [
  { name: "E2", config: TOKENS_ECDSA_V2_CONFIG },
  { name: "E3", config: TOKENS_ECDSA_V3_CONFIG },
  { name: "S2", config: TOKENS_SCHNORR_V2_CONFIG },
  { name: "S3", config: TOKENS_SCHNORR_V3_CONFIG },
];
