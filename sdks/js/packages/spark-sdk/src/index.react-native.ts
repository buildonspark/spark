/* Root React Native entrypoint */

import { setCrypto } from "./utils/crypto.js";
import { SparkFrost } from "./spark_bindings/spark-bindings.react-native.js";
import { setSparkFrostOnce } from "./spark_bindings/spark-bindings.js";

setCrypto(globalThis.crypto);
setSparkFrostOnce(new SparkFrost());

export { getSparkFrost } from "./spark_bindings/spark-bindings.js";
export { type DummyTx } from "./spark_bindings/types.js";

export * from "./errors/index.js";
export * from "./utils/index.js";

export {
  DefaultSparkSigner,
  UnsafeStatelessSparkSigner,
} from "./signer/signer.js";
export { type SparkSigner } from "./signer/signer.js";
export { SparkWallet } from "./spark-wallet/spark-wallet.react-native.js";
export * from "./spark-wallet/types.js";

export { type WalletConfigService } from "./services/config.js";
export { ConnectionManagerBrowser as ConnectionManager } from "./services/connection/connection.browser.js";
export { type ConnectionManager as BaseConnectionManager } from "./services/connection/connection.js";
export { TokenTransactionService } from "./services/token-transactions.js";
export { WalletConfig, type ConfigOptions } from "./services/wallet-config.js";
