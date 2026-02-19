import { mod } from "@noble/curves/abstract/modular";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { bytesToNumberBE, numberToBytesBE } from "@noble/curves/utils";
import type {
  AggregateFrostBindingParams,
  DummyTx,
  SignFrostBindingParams,
} from "./types.js";

export type WasmVerifiableSecretShare = {
  threshold: number;
  index: number;
  share: Uint8Array;
  proofs: Uint8Array[];
};

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

  generateAdaptorFromSignature(signature: Uint8Array): {
    adaptorSignature: Uint8Array;
    adaptorPrivateKey: Uint8Array;
  } {
    const adaptorPrivateKey = secp256k1.utils.randomPrivateKey();
    const { r, s } = parseSignature(signature);
    const sBigInt = bytesToNumberBE(s);
    const tBigInt = bytesToNumberBE(adaptorPrivateKey);
    const newS = mod(sBigInt - tBigInt, secp256k1.CURVE.n);
    const adaptorSignature = new Uint8Array([
      ...r,
      ...numberToBytesBE(newS, 32),
    ]);
    return { adaptorSignature, adaptorPrivateKey };
  }

  generateSignatureFromExistingAdaptor(
    signature: Uint8Array,
    adaptorPrivateKeyBytes: Uint8Array,
  ): Uint8Array {
    const { r, s } = parseSignature(signature);
    const sBigInt = bytesToNumberBE(s);
    const tBigInt = bytesToNumberBE(adaptorPrivateKeyBytes);
    const newS = mod(sBigInt - tBigInt, secp256k1.CURVE.n);
    return new Uint8Array([...r, ...numberToBytesBE(newS, 32)]);
  }

  validateAdaptorSignature(
    pubkey: Uint8Array,
    hash: Uint8Array,
    signature: Uint8Array,
    adaptorPubkey: Uint8Array,
  ): boolean {
    return schnorrVerifyWithAdaptor(signature, hash, pubkey, adaptorPubkey);
  }

  applyAdaptorToSignature(
    pubkey: Uint8Array,
    hash: Uint8Array,
    signature: Uint8Array,
    adaptorPrivateKeyBytes: Uint8Array,
  ): Uint8Array {
    const { r, s } = parseSignature(signature);
    const sBigInt = bytesToNumberBE(s);
    const adaptorPrivateKey = bytesToNumberBE(adaptorPrivateKeyBytes);

    const newS = mod(sBigInt + adaptorPrivateKey, secp256k1.CURVE.n);
    const newSig = new Uint8Array([...r, ...numberToBytesBE(newS, 32)]);
    try {
      if (schnorr.verify(newSig, hash, pubkey)) {
        return newSig;
      }
    } catch (_e) {
      // Addition didn't work, try subtraction
    }

    const altS = mod(sBigInt - adaptorPrivateKey, secp256k1.CURVE.n);
    const altSig = new Uint8Array([...r, ...numberToBytesBE(altS, 32)]);
    try {
      if (schnorr.verify(altSig, hash, pubkey)) {
        return altSig;
      }
    } catch (_e) {
      // Subtraction also didn't work
    }

    throw new Error("Cannot apply adaptor to signature");
  }

  abstract splitSecretWithProofs(
    secret: Uint8Array,
    threshold: number,
    numShares: number,
  ): Promise<WasmVerifiableSecretShare[]>;
  abstract recoverSecret(
    shares: { threshold: number; index: number; share: Uint8Array }[],
  ): Promise<Uint8Array>;
  abstract validateShare(
    share: Uint8Array,
    index: number,
    threshold: number,
    proofs: Uint8Array[],
  ): Promise<void>;

  // These will be moved to bindings in the future
  getPublicKeyBytes(privateKey: Uint8Array): Uint8Array {
    return secp256k1.getPublicKey(privateKey, true);
  }
  batchGetPublicKeyBytes(privateKeys: Uint8Array[]): Uint8Array[] {
    return privateKeys.map((privateKey) => this.getPublicKeyBytes(privateKey));
  }
}

function parseSignature(signature: Uint8Array): {
  r: Uint8Array;
  s: Uint8Array;
} {
  if (signature.length !== 64) {
    throw new Error(
      `Invalid signature length: expected 64, got ${signature.length}`,
    );
  }
  const r = signature.slice(0, 32);
  const s = signature.slice(32, 64);
  if (bytesToNumberBE(r) >= secp256k1.CURVE.Fp.ORDER) {
    throw new Error("Invalid signature: r >= field prime");
  }
  if (bytesToNumberBE(s) >= secp256k1.CURVE.n) {
    throw new Error("Invalid signature: s >= group order");
  }
  return { r, s };
}

function schnorrVerifyWithAdaptor(
  signature: Uint8Array,
  hash: Uint8Array,
  pubKeyBytes: Uint8Array,
  adaptorPubkey: Uint8Array,
): boolean {
  if (hash.length !== 32) {
    throw new Error(`wrong size for message (got ${hash.length}, want 32)`);
  }
  const pubKey = schnorr.utils.lift_x(bytesToNumberBE(pubKeyBytes));
  pubKey.assertValidity();
  const r = signature.slice(0, 32);
  const s = signature.slice(32, 64);
  const commitment = schnorr.utils.taggedHash(
    "BIP0340/challenge",
    r,
    pubKey.toBytes().slice(1),
    hash,
  );
  const e = mod(bytesToNumberBE(commitment), secp256k1.CURVE.n);
  const negE = mod(-e, secp256k1.CURVE.n);
  const sG = secp256k1.Point.BASE.multiplyUnsafe(bytesToNumberBE(s));
  const eP = pubKey.multiplyUnsafe(negE);
  const R = sG.add(eP);
  if (R.is0()) {
    throw new Error("R is zero");
  }
  R.assertValidity();
  const adaptorPoint = secp256k1.Point.fromHex(adaptorPubkey);
  const newR = R.add(adaptorPoint);
  if (newR.equals(secp256k1.Point.ZERO)) {
    throw new Error("calculated R point is the point at infinity");
  }
  newR.assertValidity();
  if (newR.y % 2n !== 0n) {
    throw new Error("calculated R y-value is odd");
  }
  const rNum = bytesToNumberBE(r);
  if (newR.toAffine().x !== rNum) {
    throw new Error("calculated R point was not given R");
  }
  return true;
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
