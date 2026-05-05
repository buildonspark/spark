import { SparkFrostBase } from "./spark-bindings.js";
import type {
  AggregateFrostBindingParams,
  DummyTx,
  SignFrostBindingParams,
} from "./types.js";
import { NativeModules } from "react-native";

type NativeTx = { tx: number[] };
type NativeDummyTx = NativeTx & { txid: string };
type NativeNodeTxPair = { cpfp: NativeTx; direct: NativeTx };
type NativeRefundTxTrio = {
  cpfp_refund: NativeTx;
  direct_refund?: NativeTx;
  direct_from_cpfp_refund: NativeTx;
};
type NativeSecretShare = {
  threshold: number;
  index: number;
  share: number[];
  proofs: number[][];
};
type SparkFrostNativeModule = {
  signFrost(params: unknown): Promise<number[]>;
  aggregateFrost(params: unknown): Promise<number[]>;
  createDummyTx(params: unknown): Promise<unknown>;
  encryptEcies(params: unknown): Promise<number[]>;
  decryptEcies(params: unknown): Promise<number[]>;
  constructNodeTxPair(params: unknown): Promise<NativeNodeTxPair>;
  constructRefundTxTrio(params: unknown): Promise<NativeRefundTxTrio>;
  computeMultiInputSighash(params: unknown): Promise<number[]>;
  getPublicKey(params: unknown): Promise<number[]>;
  batchGetPublicKeys(params: unknown): Promise<number[][]>;
  verifySignature(params: unknown): Promise<boolean>;
  [method: string]: ((params: unknown) => Promise<unknown>) | undefined;
};

const SparkFrostModule = NativeModules.SparkFrostModule as
  | SparkFrostNativeModule
  | undefined;

function getSparkFrostModule(): SparkFrostNativeModule {
  if (!SparkFrostModule) {
    throw new Error("SparkFrostModule is not available in this environment");
  }
  return SparkFrostModule;
}

// Helper functions for converting between Uint8Array and number[]
const toNumberArray = (arr: Uint8Array): number[] => Array.from(arr);
const toUint8Array = (arr: number[]): Uint8Array => new Uint8Array(arr);

function describeCreateDummyTxResult(result: unknown): string {
  if (result === null) {
    return "null";
  }
  if (result === undefined) {
    return "undefined";
  }
  if (Array.isArray(result)) {
    return `array(length=${result.length})`;
  }
  if (typeof result !== "object") {
    return typeof result;
  }

  const record = result as Record<string, unknown>;
  const keys = Object.keys(record).sort();
  const tx = record.tx;
  const txShape = Array.isArray(tx) ? `array(length=${tx.length})` : typeof tx;
  return `object(keys=[${keys.join(",")}], tx=${txShape}, txid=${typeof record.txid})`;
}

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
    const sparkFrostModule = getSparkFrostModule();
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

    const result = await sparkFrostModule.aggregateFrost(nativeParams);
    return toUint8Array(result);
  }

  async createDummyTx(address: string, amountSats: bigint): Promise<DummyTx> {
    if (!SparkFrostModule) {
      throw new Error("SparkFrostModule is not available in this environment");
    }

    const bridgeParams = {
      address,
      amountSats: amountSats.toString(), // JS sends string for bigint
    };

    let result: unknown;
    try {
      result = await SparkFrostModule.createDummyTx(bridgeParams);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      throw new Error(`SparkFrostModule.createDummyTx failed: ${message}`, {
        cause: error,
      });
    }

    if (
      result &&
      typeof result === "object" &&
      Array.isArray((result as { tx?: unknown }).tx) &&
      typeof (result as { txid?: unknown }).txid === "string"
    ) {
      const dummyTx = result as NativeDummyTx;
      return {
        tx: toUint8Array(dummyTx.tx),
        txid: dummyTx.txid,
      };
    }

    throw new Error(
      `Invalid result structure from createDummyTx native call: expected { tx: number[]; txid: string }, received ${describeCreateDummyTxResult(
        result,
      )}`,
    );
  }

  async encryptEcies(
    msg: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array> {
    const sparkFrostModule = getSparkFrostModule();
    const result = await sparkFrostModule.encryptEcies({
      msg: toNumberArray(msg),
      publicKey: toNumberArray(publicKey),
    });
    return toUint8Array(result);
  }

  async decryptEcies(
    encryptedMsg: Uint8Array,
    privateKey: Uint8Array,
  ): Promise<Uint8Array> {
    const sparkFrostModule = getSparkFrostModule();
    const result = await sparkFrostModule.decryptEcies({
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
    const result = await SparkFrostReactNative.callNativeModule<
      NativeSecretShare[]
    >("splitSecretWithProofs", {
      secret: toNumberArray(secret),
      threshold,
      numShares,
    });
    return result.map((s) => ({
      threshold: s.threshold,
      index: s.index,
      share: toUint8Array(s.share),
      proofs: s.proofs.map((p) => toUint8Array(p)),
    }));
  }

  async recoverSecret(
    shares: { threshold: number; index: number; share: Uint8Array }[],
  ) {
    const nativeShares = shares.map((s) => ({
      threshold: s.threshold,
      index: s.index,
      share: toNumberArray(s.share),
    }));
    const result = await SparkFrostReactNative.callNativeModule<number[]>(
      "recoverSecret",
      { shares: nativeShares },
    );
    return toUint8Array(result);
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

  async constructNodeTxPair(
    parentTx: Uint8Array,
    vout: number,
    address: string,
    sequence: number,
    directSequence: number,
    feeSats: bigint,
  ): Promise<{ cpfp: { tx: Uint8Array }; direct: { tx: Uint8Array } }> {
    if (!SparkFrostModule) {
      throw new Error("SparkFrostModule is not available in this environment");
    }
    const result = await SparkFrostModule.constructNodeTxPair({
      parentTx: toNumberArray(parentTx),
      vout,
      address,
      sequence,
      directSequence,
      feeSats: feeSats.toString(),
    });
    return {
      cpfp: { tx: toUint8Array(result.cpfp.tx) },
      direct: { tx: toUint8Array(result.direct.tx) },
    };
  }

  async constructRefundTxTrio(
    cpfpNodeTx: Uint8Array,
    directNodeTx: Uint8Array | null,
    vout: number,
    receivingPubkey: Uint8Array,
    network: string,
    sequence: number,
    directSequence: number,
    feeSats: bigint,
  ): Promise<{
    cpfp_refund: { tx: Uint8Array };
    direct_refund?: { tx: Uint8Array };
    direct_from_cpfp_refund: { tx: Uint8Array };
  }> {
    if (!SparkFrostModule) {
      throw new Error("SparkFrostModule is not available in this environment");
    }
    const result = await SparkFrostModule.constructRefundTxTrio({
      cpfpNodeTx: toNumberArray(cpfpNodeTx),
      directNodeTx: directNodeTx ? toNumberArray(directNodeTx) : null,
      vout,
      receivingPubkey: toNumberArray(receivingPubkey),
      network,
      sequence,
      directSequence,
      feeSats: feeSats.toString(),
    });
    const out: {
      cpfp_refund: { tx: Uint8Array };
      direct_refund?: { tx: Uint8Array };
      direct_from_cpfp_refund: { tx: Uint8Array };
    } = {
      cpfp_refund: { tx: toUint8Array(result.cpfp_refund.tx) },
      direct_from_cpfp_refund: {
        tx: toUint8Array(result.direct_from_cpfp_refund.tx),
      },
    };
    if (result.direct_refund) {
      out.direct_refund = { tx: toUint8Array(result.direct_refund.tx) };
    }
    return out;
  }

  async computeMultiInputSighash(
    tx: Uint8Array,
    inputIndex: number,
    prevOutScripts: Uint8Array[],
    prevOutValues: number[],
  ): Promise<Uint8Array> {
    if (!SparkFrostModule) {
      throw new Error("SparkFrostModule is not available in this environment");
    }
    const result = await SparkFrostModule.computeMultiInputSighash({
      tx: toNumberArray(tx),
      inputIndex,
      prevOutScripts: prevOutScripts.map(toNumberArray),
      prevOutValues,
    });
    return toUint8Array(result);
  }

  private static callNativeModule<T>(
    method: string,
    params: unknown,
  ): Promise<T> {
    if (!SparkFrostModule) {
      throw new Error("SparkFrostModule is not available in this environment");
    }
    const nativeMethod = SparkFrostModule[method];
    if (!nativeMethod) {
      throw new Error(`SparkFrostModule.${method} is not available`);
    }
    return nativeMethod(params) as Promise<T>;
  }

  static async getPublicKey(
    privateKey: Uint8Array,
    compressed: boolean = true,
  ): Promise<Uint8Array> {
    const sparkFrostModule = getSparkFrostModule();
    const result = await sparkFrostModule.getPublicKey({
      privateKey: toNumberArray(privateKey),
      compressed,
    });
    return toUint8Array(result);
  }

  static async batchGetPublicKeys(
    privateKeys: Uint8Array[],
    compressed: boolean = true,
  ): Promise<Uint8Array[]> {
    const sparkFrostModule = getSparkFrostModule();
    const result = await sparkFrostModule.batchGetPublicKeys({
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
    const sparkFrostModule = getSparkFrostModule();
    const result = await sparkFrostModule.verifySignature({
      signature: toNumberArray(signature),
      message: toNumberArray(message),
      publicKey: toNumberArray(publicKey),
    });
    return result;
  }
}

export { type DummyTx, SparkFrostReactNative as SparkFrost };
