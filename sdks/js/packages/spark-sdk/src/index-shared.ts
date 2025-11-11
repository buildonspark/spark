export * from "./errors/index.js";
export * from "./utils/index.js";

export { getSparkFrost } from "./spark-bindings/spark-bindings.js";

export {
  DefaultSparkSigner,
  UnsafeStatelessSparkSigner,
  type SparkSigner,
} from "./signer/signer.js";
export * from "./signer/types.js";

export { type IKeyPackage, type DummyTx } from "./spark-bindings/types.js";
export * from "./spark-wallet/types.js";

export { type WalletConfigService } from "./services/config.js";
export { TokenTransactionService } from "./services/token-transactions.js";
export { WalletConfig, type ConfigOptions } from "./services/wallet-config.js";
