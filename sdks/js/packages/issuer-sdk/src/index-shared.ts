export {
  DefaultSparkSigner,
  UnsafeStatelessSparkSigner,
  type SparkSigner,
} from "@buildonspark/spark-sdk";
export {
  type SigningCommitmentWithOptionalNonce,
  type SigningNonce,
  type SigningCommitment,
  type KeyDerivationType,
  type KeyDerivation,
  type SignFrostParams,
  type AggregateFrostParams,
  type SplitSecretWithProofsParams,
  type DerivedHDKey,
  type KeyPair,
  type SubtractSplitAndEncryptParams,
  type SubtractSplitAndEncryptResult,
} from "@buildonspark/spark-sdk";

export * from "./issuer-wallet/types.js";
export { type IKeyPackage, type DummyTx } from "@buildonspark/spark-sdk";

export { type WalletConfigService } from "@buildonspark/spark-sdk";
export { WalletConfig, type ConfigOptions } from "@buildonspark/spark-sdk";
