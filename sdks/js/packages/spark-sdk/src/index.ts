/* Root web/default entrypoint. For Node.js see index.node.ts */

import { setCrypto } from "./utils/crypto.js";

const cryptoImpl =
  typeof window !== "undefined" && window.crypto
    ? window.crypto
    : typeof globalThis !== "undefined" && globalThis.crypto
      ? globalThis.crypto
      : null;

setCrypto(cryptoImpl);

export * from "./errors/index.js";
export * from "./utils/index.js";

export {
  DefaultSparkSigner,
  TaprootOutputKeysGenerator,
  TaprootSparkSigner,
  UnsafeStatelessSparkSigner,
  type SparkSigner,
} from "./signer/signer.js";
export * from "./signer/types.js";
export { type IKeyPackage } from "./spark_bindings/types.js";

export {
  SparkWalletBrowser as SparkWallet,
  initializeTracerEnvBrowser as initializeTracerEnv,
} from "./spark-wallet/spark-wallet.browser.js";
export * from "./spark-wallet/types.js";

export { ConnectionManagerBrowser as ConnectionManager } from "./services/connection/connection.browser.js";
export { type ConnectionManager as BaseConnectionManager } from "./services/connection/connection.js";
export { type WalletConfigService } from "./services/config.js";
export { TokenTransactionService } from "./services/token-transactions.js";
export { WalletConfig, type ConfigOptions } from "./services/wallet-config.js";
