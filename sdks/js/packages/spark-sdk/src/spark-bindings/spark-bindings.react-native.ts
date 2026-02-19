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

  async splitSecretWithProofs(
    secret: Uint8Array,
    threshold: number,
    numShares: number,
  ) {
    const result = await SparkFrostReactNative.callNativeModule(
      "splitSecretWithProofs",
      {
        secret: toNumberArray(secret),
        threshold,
        numShares,
      },
    );
    return (
      result as {
        threshold: number;
        index: number;
        share: number[];
        proofs: number[][];
      }[]
    ).map(
      (s: {
        threshold: number;
        index: number;
        share: number[];
        proofs: number[][];
      }) => ({
        threshold: s.threshold,
        index: s.index,
        share: toUint8Array(s.share),
        proofs: s.proofs.map((p: number[]) => toUint8Array(p)),
      }),
    );
  }

  async recoverSecret(
    shares: { threshold: number; index: number; share: Uint8Array }[],
  ) {
    const nativeShares = shares.map((s) => ({
      threshold: s.threshold,
      index: s.index,
      share: toNumberArray(s.share),
    }));
    const result = await SparkFrostReactNative.callNativeModule(
      "recoverSecret",
      { shares: nativeShares },
    );
    return toUint8Array(result as number[]);
  }

  async validateShare(
    share: Uint8Array,
    index: number,
    threshold: number,
    proofs: Uint8Array[],
  ) {
    await SparkFrostReactNative.callNativeModule("validateShare", {
      share: toNumberArray(share),
      index,
      threshold,
      proofs: proofs.map(toNumberArray),
    });
  }

  private static async callNativeModule(
    method: string,
    params: any,
  ): Promise<any> {
    if (!SparkFrostModule) {
      throw new Error("SparkFrostModule is not available in this environment");
    }
    return SparkFrostModule[method](params);
  }

  static async getPublicKey(
    privateKey: Uint8Array,
    compressed: boolean = true,
  ): Promise<Uint8Array> {
    const result = await SparkFrostModule.getPublicKey({
      privateKey: toNumberArray(privateKey),
      compressed,
    });
    return toUint8Array(result);
  }

  static async batchGetPublicKeys(
    privateKeys: Uint8Array[],
    compressed: boolean = true,
  ): Promise<Uint8Array[]> {
    const result = await SparkFrostModule.batchGetPublicKeys({
      privateKeys: privateKeys.map(toNumberArray),
      compressed,
    });
    return result.map(toUint8Array);
  }

  static async verifySignature(
    signature: Uint8Array,
    message: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<boolean> {
    const result = await SparkFrostModule.verifySignature({
      signature: toNumberArray(signature),
      message: toNumberArray(message),
      publicKey: toNumberArray(publicKey),
    });
    return result;
  }
}

export { type DummyTx, SparkFrostReactNative as SparkFrost };
