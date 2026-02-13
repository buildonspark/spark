import "../buffer.js";

import { SparkFrost } from "./spark-bindings/spark-bindings.browser.js";
import { setSparkFrostOnce } from "./spark-bindings/spark-bindings.js";

import { setCrypto } from "./utils/crypto.js";

const cryptoImpl =
  typeof window !== "undefined" && window.crypto
    ? window.crypto
    : typeof globalThis !== "undefined" && globalThis.crypto
      ? globalThis.crypto
      : null;

setCrypto(cryptoImpl);
setSparkFrostOnce(new SparkFrost());

export * from "./index-shared.js";

export { ConnectionManagerBrowser as ConnectionManager } from "./services/connection/connection.browser.js";
export { type ConnectionManager as BaseConnectionManager } from "./services/connection/connection.js";
export { SparkReadonlyClientBrowser as SparkReadonlyClient } from "./spark-readonly-client/spark-readonly-client.browser.js";
export { SparkWalletBrowser as SparkWallet } from "./spark-wallet/spark-wallet.browser.js";
