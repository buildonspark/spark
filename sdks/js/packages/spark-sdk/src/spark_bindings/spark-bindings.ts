import type {
  SignFrostBindingParams,
  AggregateFrostBindingParams,
  DummyTx,
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
