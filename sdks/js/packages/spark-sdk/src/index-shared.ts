export * from "./errors/index.js";
export * from "./utils/index.js";

export { getSparkFrost } from "./spark-bindings/spark-bindings.js";

export {
  DefaultSparkSigner,
  UnsafeStatelessSparkSigner,
  type SparkSigner,
} from "./signer/signer.js";
export {
  type SigningCommitmentWithOptionalNonce,
  type SigningNonce,
  type SigningCommitment,
  KeyDerivationType,
  type KeyDerivation,
  type SignFrostParams,
  type AggregateFrostParams,
  type SplitSecretWithProofsParams,
  type DerivedHDKey,
  type KeyPair,
  type SubtractSplitAndEncryptParams,
  type SubtractSplitAndEncryptResult,
} from "./signer/types.js";

export { type IKeyPackage, type DummyTx } from "./spark-bindings/types.js";
export * from "./spark-readonly-client/types.js";
export * from "./spark-wallet/types.js";

export { type WalletConfigService } from "./services/config.js";
export { TokenTransactionService } from "./services/tokens/token-transactions.js";
export {
  WalletConfig,
  createLocalSigningOperators,
  getElectrsUrl,
  getLocalSigningOperators,
  getLocalSigningThreshold,
  getSspIdentityPublicKey,
  getSspSchemaEndpoint,
  mergeConfigOptionsForNetwork,
  normalizeNetworkType,
  rewriteSigningOperatorAddresses,
  type ConfigOptions,
  type SigningOperator,
} from "./services/wallet-config.js";
