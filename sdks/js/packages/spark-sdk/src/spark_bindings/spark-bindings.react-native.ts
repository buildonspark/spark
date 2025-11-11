import { SparkFrostBase } from "./spark-bindings.js";
import type {
  AggregateFrostBindingParams,
  DummyTx,
  SignFrostBindingParams,
} from "./types.js";
// Get SparkFrostModule from React Native if available
import { NativeModules } from "react-native";
const { SparkFrostModule } = NativeModules;

// Helper functions for converting between Uint8Array and number[]
const toNumberArray = (arr: Uint8Array): number[] => Array.from(arr);
const toUint8Array = (arr: number[]): Uint8Array => new Uint8Array(arr);

class SparkFrostReactNative extends SparkFrostBase {
  async signFrost(params: SignFrostBindingParams) {
    if (!SparkFrostModule) {
      throw new Error("NativeSparkFrost is not available in this environment");
    }
    const nativeParams = {
      msg: toNumberArray(params.message),
      keyPackage: {
        secretKey: toNumberArray(params.keyPackage.secretKey),
        publicKey: toNumberArray(params.keyPackage.publicKey),
        verifyingKey: toNumberArray(params.keyPackage.verifyingKey),
      },
      nonce: {
        hiding: toNumberArray(params.nonce.hiding),
        binding: toNumberArray(params.nonce.binding),
      },
      selfCommitment: {
        hiding: toNumberArray(params.selfCommitment.hiding),
        binding: toNumberArray(params.selfCommitment.binding),
      },
      statechainCommitments: Object.fromEntries(
        Object.entries(params.statechainCommitments ?? {}).map(([k, v]) => [
          k,
          {
            hiding: toNumberArray(v.hiding),
            binding: toNumberArray(v.binding),
          },
        ]),
      ),
      adaptorPubKey: params.adaptorPubKey
        ? toNumberArray(params.adaptorPubKey)
        : undefined,
    };

    const result = await SparkFrostModule.signFrost(nativeParams);
    return toUint8Array(result);
  }

  async aggregateFrost(params: AggregateFrostBindingParams) {
    const nativeParams = {
      msg: toNumberArray(params.message),
      statechainCommitments: Object.fromEntries(
        Object.entries(params.statechainCommitments ?? {}).map(([k, v]) => [
          k,
          {
            hiding: toNumberArray(v.hiding),
            binding: toNumberArray(v.binding),
          },
        ]),
      ),
      selfCommitment: {
        hiding: toNumberArray(params.selfCommitment.hiding),
        binding: toNumberArray(params.selfCommitment.binding),
      },
      statechainSignatures: Object.fromEntries(
        Object.entries(params.statechainSignatures ?? {}).map(([k, v]) => [
          k,
          toNumberArray(v),
        ]),
      ),
      selfSignature: toNumberArray(params.selfSignature),
      statechainPublicKeys: Object.fromEntries(
        Object.entries(params.statechainPublicKeys ?? {}).map(([k, v]) => [
          k,
          toNumberArray(v),
        ]),
      ),
      selfPublicKey: toNumberArray(params.selfPublicKey),
      verifyingKey: toNumberArray(params.verifyingKey),
      adaptorPubKey: params.adaptorPubKey
        ? toNumberArray(params.adaptorPubKey)
        : undefined,
    };

    const result = await SparkFrostModule.aggregateFrost(nativeParams);
    return toUint8Array(result);
  }

  async createDummyTx(address: string, amountSats: bigint): Promise<DummyTx> {
    if (!SparkFrostModule) {
      console.error("NativeSparkFrost.ts: SparkFrostModule is not available.");
      throw new Error("SparkFrostModule is not available");
    }
    try {
      const bridgeParams = {
        address,
        amountSats: amountSats.toString(), // JS sends string for bigint
      };
      const result = await SparkFrostModule.createDummyTx(bridgeParams);

      if (
        result &&
        Array.isArray(result.tx) &&
        typeof result.txid === "string"
      ) {
        return {
          tx: toUint8Array(result.tx as number[]),
          txid: result.txid,
        };
      } else {
        console.error(
          "NativeSparkFrost.ts: Invalid result structure from native call. Result:",
          result,
        );
        throw new Error(
          "Invalid result structure from createDummyTx native call",
        );
      }
    } catch (e) {
      console.error(
        "NativeSparkFrost.ts: Error during SparkFrostModule.createDummyTx call:",
        e,
      );
      throw e;
    }
  }

  async encryptEcies(
    msg: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array> {
    const result = await SparkFrostModule.encryptEcies({
      msg: toNumberArray(msg),
      publicKey: toNumberArray(publicKey),
    });
    return toUint8Array(result);
  }

  async decryptEcies(
    encryptedMsg: Uint8Array,
    privateKey: Uint8Array,
  ): Promise<Uint8Array> {
    const result = await SparkFrostModule.decryptEcies({
      encryptedMsg: toNumberArray(encryptedMsg),
      privateKey: toNumberArray(privateKey),
    });
    return toUint8Array(result);
  }
}

export { type DummyTx, SparkFrostReactNative as SparkFrost };
