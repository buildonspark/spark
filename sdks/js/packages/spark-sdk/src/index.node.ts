/* Root Node.js entrypoint */

import nodeCrypto from "crypto";

import { setCrypto } from "./utils/crypto.js";
import { setSparkFrostOnce } from "./spark-bindings/spark-bindings.js";
import { SparkFrost } from "./spark-bindings/spark-bindings.node.js";

const cryptoImpl =
  typeof global !== "undefined" && global.crypto ? global.crypto : nodeCrypto;

setCrypto(cryptoImpl);
setSparkFrostOnce(new SparkFrost());

export * from "./index-shared.js";

export { SparkWalletNodeJS as SparkWallet } from "./spark-wallet/spark-wallet.node.js";
export { initializeTracerEnv } from "./otel/initializeTracerEnv.node.js";
export { ConnectionManagerNodeJS as ConnectionManager } from "./services/connection/connection.node.js";
export { type ConnectionManager as BaseConnectionManager } from "./services/connection/connection.js";
