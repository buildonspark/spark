/* Root React Native entrypoint */
import "../buffer.js";

import { Platform } from "react-native";

import { setReactNativeEnvDetails } from "./constants.js";
import { setSparkFrostOnce } from "./spark-bindings/spark-bindings.js";
import { SparkFrost } from "./spark-bindings/spark-bindings.react-native.js";
import { setCrypto } from "./utils/crypto.js";

const rv = Platform.constants?.reactNativeVersion;
if (rv) {
  setReactNativeEnvDetails(
    `${rv.major}.${rv.minor}.${rv.patch}`,
    Platform.OS,
    Platform.Version,
  );
}

setCrypto(globalThis.crypto);
setSparkFrostOnce(new SparkFrost());

export * from "./index-shared.js";

export { ConnectionManagerBrowser as ConnectionManager } from "./services/connection/connection.browser.js";
export { type ConnectionManager as BaseConnectionManager } from "./services/connection/connection.js";
export { SparkReadonlyClientReactNative as SparkReadonlyClient } from "./spark-readonly-client/spark-readonly-client.react-native.js";
export { SparkWallet } from "./spark-wallet/spark-wallet.react-native.js";
