import { mod } from "@noble/curves/abstract/modular";
import { bytesToNumberBE, numberToBytesBE } from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { ValidationError } from "../errors/index.js";

export function generateSignatureFromExistingAdaptor(
  signature: Uint8Array,
  adaptorPrivateKeyBytes: Uint8Array,
): Uint8Array {
  const { r, s } = parseSignature(signature);

  const sBigInt = bytesToNumberBE(s);
  const tBigInt = bytesToNumberBE(adaptorPrivateKeyBytes);

  const newS = mod(sBigInt - tBigInt, secp256k1.CURVE.n);

  const newSignature = new Uint8Array([...r, ...numberToBytesBE(newS, 32)]);

  return newSignature;
}

export function generateAdaptorFromSignature(signature: Uint8Array): {
  adaptorSignature: Uint8Array;
  adaptorPrivateKey: Uint8Array;
} {
  const adaptorPrivateKey = secp256k1.utils.randomPrivateKey();

  const { r, s } = parseSignature(signature);

  const sBigInt = bytesToNumberBE(s);
  const tBigInt = bytesToNumberBE(adaptorPrivateKey);

  // Calculate s - adaptorPrivateKey
  const newS = mod(sBigInt - tBigInt, secp256k1.CURVE.n);

  const newSignature = new Uint8Array([...r, ...numberToBytesBE(newS, 32)]);

  return {
    adaptorSignature: newSignature,
    adaptorPrivateKey: adaptorPrivateKey,
  };
}

export function validateOutboundAdaptorSignature(
  pubkey: Uint8Array,
  hash: Uint8Array,
  signature: Uint8Array,
  adaptorPubkey: Uint8Array,
): boolean {
  return schnorrVerifyWithAdaptor(
    signature,
    hash,
    pubkey,
    adaptorPubkey,
    false,
  );
}

export function applyAdaptorToSignature(
  pubkey: Uint8Array,
  hash: Uint8Array,
  signature: Uint8Array,
  adaptorPrivateKeyBytes: Uint8Array,
): Uint8Array {
  // Parse the signature
  const { r, s } = parseSignature(signature);

  // Convert values to bigints
  const sBigInt = bytesToNumberBE(s);
  const adaptorPrivateKey = bytesToNumberBE(adaptorPrivateKeyBytes);

  // Try adding adaptor to s first
  const newS = mod(sBigInt + adaptorPrivateKey, secp256k1.CURVE.n);
  const newSig = new Uint8Array([...r, ...numberToBytesBE(newS, 32)]);

  try {
    if (schnorr.verify(newSig, hash, pubkey)) {
      return newSig;
    }
  } catch (e) {
    console.error("[applyAdaptorToSignature] Addition verification failed:", e);
  }

  // If adding didn't work, try subtracting
  const altS = mod(sBigInt - adaptorPrivateKey, secp256k1.CURVE.n);
  const altSig = new Uint8Array([...r, ...numberToBytesBE(altS, 32)]);
  try {
    if (schnorr.verify(altSig, hash, pubkey)) {
      return altSig;
    }
  } catch (e) {
    console.error(
      "[applyAdaptorToSignature] Subtraction verification failed:",
      e,
    );
  }

  throw new Error("Cannot apply adaptor to signature");
}

// Step 1: P = lift_x(int(pk))
// Step 2: r = int(sig[0:32])
// Step 3: s = int(sig[32:64])
// Step 4: e = int(tagged_hash("BIP0340/challenge", bytes(r) || bytes(P) || m)) mod n
// Step 5: R = sG - eP
// Step 6: R = R + T
function schnorrVerifyWithAdaptor(
  signature: Uint8Array,
  hash: Uint8Array,
  pubKeyBytes: Uint8Array,
  adaptorPubkey: Uint8Array,
  inbound: boolean,
): boolean {
  // Step 1: Verify message length
  if (hash.length !== 32) {
    throw new Error(`wrong size for message (got ${hash.length}, want 32)`);
  }

  // Step 2: Lift x coordinate to curve point
  const pubKey = schnorr.utils.lift_x(bytesToNumberBE(pubKeyBytes));
  pubKey.assertValidity();

  // Parse signature
  // Step 3 and 4 is handled by parseSignature
  const { r, s } = parseSignature(signature);

  // Step 5: Compute challenge.
  const commitmenet = schnorr.utils.taggedHash(
    "BIP0340/challenge",
    r,
    pubKey.toBytes().slice(1),
    hash,
  );
  if (commitmenet.length > 32) {
    throw new Error("hash of (r || P || m) too big");
  }

  const e = mod(bytesToNumberBE(commitmenet), secp256k1.CURVE.n);
  const negE = mod(-e, secp256k1.CURVE.n); // Negate e before multiplication

  // Step 6: Calculate R = sG - eP
  const sG = secp256k1.Point.BASE.multiplyUnsafe(bytesToNumberBE(s));
  const eP = pubKey.multiplyUnsafe(negE);
  const R = sG.add(eP);

  if (R.is0()) {
    throw new Error("R is zero");
  }

  R.assertValidity();

  // Step 6.5: Add adaptor public key T to R
  const adaptorPoint = secp256k1.Point.fromHex(adaptorPubkey);
  const newR = R.add(adaptorPoint);

  // Step 7: Check for point at infinity (if not inbound)
  if (!inbound && newR.equals(secp256k1.Point.ZERO)) {
    throw new Error("calculated R point is the point at infinity");
  }

  // Step 8: Check if R.y is odd
  newR.assertValidity();
  if (newR.y % 2n !== 0n) {
    throw new Error("calculated R y-value is odd");
  }

  // Step 9: Check if R.x == r
  const rNum = bytesToNumberBE(r);
  if (newR.toAffine().x !== rNum) {
    throw new Error("calculated R point was not given R");
  }

  return true;
}

function parseSignature(signature: Uint8Array): {
  r: Uint8Array;
  s: Uint8Array;
} {
  if (signature.length < 64) {
    throw new ValidationError("Signature too short", {
      expectedLength: 64,
      actualLength: signature.length,
    });
  }
  if (signature.length > 64) {
    throw new ValidationError("Signature too long", {
      expectedLength: 64,
      actualLength: signature.length,
    });
  }

  const r = signature.slice(0, 32);
  const s = signature.slice(32, 64);

  if (bytesToNumberBE(r) >= secp256k1.CURVE.Fp.ORDER) {
    throw new ValidationError("Invalid signature: r >= field prime", {
      rValue: bytesToNumberBE(r),
      fieldPrime: secp256k1.CURVE.Fp.ORDER,
    });
  }

  if (bytesToNumberBE(s) >= secp256k1.CURVE.n) {
    throw new ValidationError("Invalid signature: s >= group order", {
      sValue: bytesToNumberBE(s),
      groupOrder: secp256k1.CURVE.n,
    });
  }

  return { r, s };
}
