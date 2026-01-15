/* Root React Native entrypoint */
import "../buffer.js";

import { setCrypto } from "./utils/crypto.js";
import { SparkFrost } from "./spark-bindings/spark-bindings.react-native.js";
import { setSparkFrostOnce } from "./spark-bindings/spark-bindings.js";

setCrypto(globalThis.crypto);
setSparkFrostOnce(new SparkFrost());

export * from "./index-shared.js";

export { SparkWallet } from "./spark-wallet/spark-wallet.react-native.js";
export { ConnectionManagerBrowser as ConnectionManager } from "./services/connection/connection.browser.js";
export { type ConnectionManager as BaseConnectionManager } from "./services/connection/connection.js";
