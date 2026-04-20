/* Root Node.js entrypoint */

import nodeCrypto from "crypto";

import { setSparkFrostOnce } from "./spark-bindings/spark-bindings.js";
import { SparkFrost } from "./spark-bindings/spark-bindings.node.js";
import { setSparkTokenPrimitivesOnce } from "./token-primitives-bindings/token-primitives-bindings.js";
import { SparkTokenPrimitives } from "./token-primitives-bindings/token-primitives-bindings.node.js";
import { setCrypto } from "./utils/crypto.js";

const cryptoImpl = nodeCrypto.webcrypto;

setCrypto(cryptoImpl);
setSparkFrostOnce(new SparkFrost());
setSparkTokenPrimitivesOnce(new SparkTokenPrimitives());

export * from "./index-shared.js";

export { type ConnectionManager as BaseConnectionManager } from "./services/connection/connection.js";
export { ConnectionManagerNodeJS as ConnectionManager } from "./services/connection/connection.node.js";
export { SparkReadonlyClientNodeJS as SparkReadonlyClient } from "./spark-readonly-client/spark-readonly-client.node.js";
export { SparkWalletNodeJS as SparkWallet } from "./spark-wallet/spark-wallet.node.js";
