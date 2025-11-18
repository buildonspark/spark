import { secp256k1 } from "@noble/curves/secp256k1";
import type {
  AggregateFrostBindingParams,
  DummyTx,
  SignFrostBindingParams,
} from "./types.js";

export abstract class SparkFrostBase {
  abstract signFrost(params: SignFrostBindingParams): Promise<Uint8Array>;
  abstract aggregateFrost(
    params: AggregateFrostBindingParams,
  ): Promise<Uint8Array>;
  abstract createDummyTx(address: string, amountSats: bigint): Promise<DummyTx>;
  abstract encryptEcies(
    msg: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array>;
  abstract decryptEcies(
    encryptedMsg: Uint8Array,
    privateKey: Uint8Array,
  ): Promise<Uint8Array>;

  // These will be moved to bindings in the future
  getPublicKeyBytes(privateKey: Uint8Array): Uint8Array {
    return secp256k1.getPublicKey(privateKey, true);
  }
  batchGetPublicKeyBytes(privateKeys: Uint8Array[]): Uint8Array[] {
    return privateKeys.map((privateKey) => this.getPublicKeyBytes(privateKey));
  }
}

let sparkFrost: SparkFrostBase | null = null;

export function setSparkFrostOnce(sparkFrostParam: SparkFrostBase) {
  if (sparkFrost) {
    /* SparkFrost should only be set once from main entrypoints, avoid
       setting it again when entrypoints are imported more than once: */
    return;
  }
  sparkFrost = sparkFrostParam;
}

export function getSparkFrost() {
  if (!sparkFrost) {
    throw new Error("sparkFrost is not set");
  }
  return sparkFrost;
}
