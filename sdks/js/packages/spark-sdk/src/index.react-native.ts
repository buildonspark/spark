/* Root React Native entrypoint */
import "../buffer.js";

import { setSparkFrostOnce } from "./spark-bindings/spark-bindings.js";
import { SparkFrost } from "./spark-bindings/spark-bindings.react-native.js";
import { setCrypto } from "./utils/crypto.js";

setCrypto(globalThis.crypto);
setSparkFrostOnce(new SparkFrost());

export * from "./index-shared.js";

export { ConnectionManagerBrowser as ConnectionManager } from "./services/connection/connection.browser.js";
export { type ConnectionManager as BaseConnectionManager } from "./services/connection/connection.js";
export { SparkReadonlyClientReactNative as SparkReadonlyClient } from "./spark-readonly-client/spark-readonly-client.react-native.js";
export { SparkWallet } from "./spark-wallet/spark-wallet.react-native.js";
