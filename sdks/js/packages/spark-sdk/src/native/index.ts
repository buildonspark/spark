/* Root React Native entrypoint */

import { setCrypto } from "../utils/crypto.js";

setCrypto(globalThis.crypto);

export * from "../errors/index.js";
export * from "../utils/index.js";

export { ReactNativeSparkSigner } from "../signer/signer.react-native.js";
/* Enable some consumers to use named import DefaultSparkSigner regardless of module, see LIG-7662 */
export { ReactNativeSparkSigner as DefaultSparkSigner } from "../signer/signer.react-native.js";

export { SparkWallet } from "../spark-wallet/spark-wallet.react-native.js";
export * from "../spark-wallet/types.js";

export { type WalletConfigService } from "../services/config.js";
export { ConnectionManagerBrowser as ConnectionManager } from "../services/connection/connection.browser.js";
export { type ConnectionManager as BaseConnectionManager } from "../services/connection/connection.js";
export { TokenTransactionService } from "../services/token-transactions.js";
export { WalletConfig, type ConfigOptions } from "../services/wallet-config.js";
