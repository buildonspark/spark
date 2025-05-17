import { isReactNative } from "../constants.js";
import type {
  AggregateFrostParams,
  DummyTx,
  SignFrostParams,
} from "./types.js";

// Detect environment and use appropriate bindings

const bindings = (() => {
  try {
    // Check if we're in a React Native environment
    if (isReactNative) {
      return require("./native/index.js").NativeSparkFrost;
    }
  } catch (e) {
    // If React Native is not available, we're in Node.js
    return require("./wasm/index.js");
  }
  // Default to WASM for Node.js
  return require("./wasm/index.js");
})();

export class SparkFrost {
  static async signFrost(params: SignFrostParams): Promise<Uint8Array> {
    return bindings.signFrost(params);
  }

  static async aggregateFrost(
    params: AggregateFrostParams,
  ): Promise<Uint8Array> {
    return bindings.aggregateFrost(params);
  }

  static async createDummyTx(
    address: string,
    amountSats: bigint,
  ): Promise<DummyTx> {
    return bindings.createDummyTx(address, amountSats);
  }

  static async encryptEcies(
    msg: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array> {
    return bindings.encryptEcies(msg, publicKey);
  }

  static async decryptEcies(
    encryptedMsg: Uint8Array,
    privateKey: Uint8Array,
  ): Promise<Uint8Array> {
    return bindings.decryptEcies(encryptedMsg, privateKey);
  }
}

export * from "./types.js";
export default SparkFrost;
