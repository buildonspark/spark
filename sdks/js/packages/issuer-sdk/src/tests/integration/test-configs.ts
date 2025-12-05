import { ConfigOptions, WalletConfig } from "@buildonspark/spark-sdk";

export const TOKENS_SCHNORR_V2_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "SCHNORR",
  tokenTransactionVersion: "V2",
};

export const TOKENS_SCHNORR_V3_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "SCHNORR",
  tokenTransactionVersion: "V3",
};

export const TOKENS_ECDSA_V2_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "ECDSA",
  tokenTransactionVersion: "V2",
};

export const TOKENS_ECDSA_V3_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "ECDSA",
  tokenTransactionVersion: "V3",
};

export const TEST_CONFIGS = [
  { name: "E2", config: TOKENS_ECDSA_V2_CONFIG },
  { name: "E3", config: TOKENS_ECDSA_V3_CONFIG },
  { name: "S2", config: TOKENS_SCHNORR_V2_CONFIG },
  { name: "S3", config: TOKENS_SCHNORR_V3_CONFIG },
];
