import { secp256k1 } from "@noble/curves/secp256k1";
import { numberToBytesBE } from "@noble/curves/utils";
import { ValidationError } from "../errors/index.js";

export function addPublicKeys(a: Uint8Array, b: Uint8Array): Uint8Array {
  if (a.length !== 33 || b.length !== 33) {
    throw new ValidationError("Public keys must be 33 bytes", {
      field: "publicKeys",
      value: `a: ${a.length}, b: ${b.length}`,
      expected: 33,
    });
  }
  const pubkeyA = secp256k1.Point.fromHex(a);
  const pubkeyB = secp256k1.Point.fromHex(b);
  return pubkeyA.add(pubkeyB).toBytes(true);
}

export function applyAdditiveTweakToPublicKey(
  pubkey: Uint8Array,
  tweak: Uint8Array,
) {
  if (pubkey.length !== 33) {
    throw new ValidationError("Public key must be 33 bytes", {
      field: "pubkey",
      value: pubkey.length,
      expected: 33,
    });
  }
  if (tweak.length !== 32) {
    throw new ValidationError("Tweak must be 32 bytes", {
      field: "tweak",
      value: tweak.length,
      expected: 32,
    });
  }
  const pubkeyPoint = secp256k1.Point.fromHex(pubkey);

  const privTweek = secp256k1.utils.normPrivateKeyToScalar(tweak);
  const pubTweek = secp256k1.getPublicKey(privTweek, true);
  const tweekPoint = secp256k1.Point.fromHex(pubTweek);

  return pubkeyPoint.add(tweekPoint).toBytes(true);
}

export function subtractPublicKeys(a: Uint8Array, b: Uint8Array) {
  if (a.length !== 33 || b.length !== 33) {
    throw new ValidationError("Public keys must be 33 bytes", {
      field: "publicKeys",
      value: `a: ${a.length}, b: ${b.length}`,
      expected: 33,
    });
  }

  const pubkeyA = secp256k1.Point.fromHex(a);
  const pubkeyB = secp256k1.Point.fromHex(b);
  return pubkeyA.subtract(pubkeyB).toBytes(true);
}

export function addPrivateKeys(a: Uint8Array, b: Uint8Array) {
  if (a.length !== 32 || b.length !== 32) {
    throw new ValidationError("Private keys must be 32 bytes", {
      field: "privateKeys",
      value: `a: ${a.length}, b: ${b.length}`,
      expected: 32,
    });
  }

  // Convert private keys to scalars (big integers)
  const privA = secp256k1.utils.normPrivateKeyToScalar(a);
  const privB = secp256k1.utils.normPrivateKeyToScalar(b);

  // Add the scalars and reduce modulo the curve order
  const sum = (privA + privB) % secp256k1.CURVE.n;

  // Convert back to bytes
  return numberToBytesBE(sum, 32);
}

export function subtractPrivateKeys(a: Uint8Array, b: Uint8Array) {
  if (a.length !== 32 || b.length !== 32) {
    throw new ValidationError("Private keys must be 32 bytes", {
      field: "privateKeys",
      value: `a: ${a.length}, b: ${b.length}`,
      expected: 32,
    });
  }

  const privA = secp256k1.utils.normPrivateKeyToScalar(a);
  const privB = secp256k1.utils.normPrivateKeyToScalar(b);
  const sum = (secp256k1.CURVE.n - privB + privA) % secp256k1.CURVE.n;

  return numberToBytesBE(sum, 32);
}

export function sumOfPrivateKeys(keys: Uint8Array[]) {
  return keys.reduce((sum, key) => {
    if (key.length !== 32) {
      throw new ValidationError("Private keys must be 32 bytes", {
        field: "privateKey",
        value: key.length,
        expected: 32,
      });
    }
    return addPrivateKeys(sum, key);
  });
}

export function lastKeyWithTarget(target: Uint8Array, keys: Uint8Array[]) {
  if (target.length !== 32) {
    throw new ValidationError("Target must be 32 bytes", {
      field: "target",
      value: target.length,
      expected: 32,
    });
  }

  const sum = sumOfPrivateKeys(keys);
  return subtractPrivateKeys(target, sum);
}
