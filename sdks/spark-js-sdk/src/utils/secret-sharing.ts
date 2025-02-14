import { equalBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import crypto from "crypto";

type Polynomial = {
  fieldModulus: bigint;
  coefficients: bigint[];
  proofs: Uint8Array[];
};

type SecretShare = {
  fieldModulus: bigint;
  threshold: number;
  index: bigint;
  share: bigint;
};

export type VerifiableSecretShare = SecretShare & {
  proofs: Uint8Array[];
};

// Generate a secure random bigint using crypto.getRandomValues
export function getRandomBigInt(max: bigint): bigint {
  const byteLength = (max.toString(2).length + 7) >> 3;
  const maxBigInt = max;

  const mask = (1n << BigInt(max.toString(2).length)) - 1n;

  while (true) {
    const randBytes = new Uint8Array(byteLength + 1);
    crypto.getRandomValues(randBytes);

    let randValue =
      BigInt("0x" + Buffer.from(randBytes).toString("hex")) & mask;

    if (randValue < maxBigInt) {
      return randValue;
    }
  }
}

// Modular inverse using extended euclidean algorithm
export function modInverse(a: bigint, m: bigint): bigint {
  // Handle negative numbers by making them positive
  a = ((a % m) + m) % m;

  let [old_r, r] = [a, m];
  let [old_s, s] = [1n, 0n];
  let [old_t, t] = [0n, 1n];

  while (r !== 0n) {
    const quotient = old_r / r;
    [old_r, r] = [r, old_r - quotient * r];
    [old_s, s] = [s, old_s - quotient * s];
    [old_t, t] = [t, old_t - quotient * t];
  }

  if (old_r !== 1n) {
    throw new Error("Modular inverse does not exist");
  }

  return ((old_s % m) + m) % m;
}

// Evaluates a polynomial at a given point
export function evaluatePolynomial(polynomial: Polynomial, x: bigint): bigint {
  let result = 0n;
  for (let i = 0; i < polynomial.coefficients.length; i++) {
    const coeff = polynomial.coefficients[i];

    const xPow = x ** BigInt(i) % polynomial.fieldModulus;

    result = (result + xPow * coeff) % polynomial.fieldModulus;
  }
  return result;
}

// Divides two numbers in a given field modulus
export function fieldDiv(
  numerator: bigint,
  denominator: bigint,
  fieldModulus: bigint
): bigint {
  if (denominator === 0n) {
    throw new Error("Division by zero");
  }

  const inverse = modInverse(denominator, fieldModulus);
  return (numerator * inverse) % fieldModulus;
}

// Computes the Lagrange coefficient for a given index and a set of points
export function computerLagrangeCoefficients(
  index: bigint,
  points: SecretShare[]
) {
  let numerator = 1n;
  let denominator = 1n;
  let fieldModulus = points[0].fieldModulus;

  for (const point of points) {
    if (point.index === index) {
      continue;
    }
    numerator = numerator * point.index;
    const value = point.index - index;
    denominator = denominator * value;
  }

  return fieldDiv(numerator, denominator, fieldModulus);
}

// Generates a polynomial for secret sharing
export function generatePolynomialForSecretSharing(
  fieldModulus: bigint,
  secret: bigint,
  degree: number
): Polynomial {
  const coefficients: bigint[] = new Array(degree);
  const proofs: Uint8Array[] = new Array(degree);

  coefficients[0] = secret;
  proofs[0] = secp256k1.ProjectivePoint.fromPrivateKey(secret).toRawBytes(true);

  for (let i = 1; i < degree; i++) {
    coefficients[i] = getRandomBigInt(fieldModulus);
    proofs[i] = secp256k1.ProjectivePoint.fromPrivateKey(
      coefficients[i]
    ).toRawBytes(true);
  }
  return {
    fieldModulus,
    coefficients,
    proofs: proofs,
  };
}

// Splits a secret into a list of shares
export function splitSecret(
  fieldModulus: bigint,
  secret: bigint,
  threshold: number,
  numberOfShares: number
) {
  const polynomial = generatePolynomialForSecretSharing(
    fieldModulus,
    secret,
    threshold
  );

  const shares: SecretShare[] = [];
  for (let i = 1; i <= numberOfShares; i++) {
    const share = evaluatePolynomial(polynomial, BigInt(i));
    shares.push({
      fieldModulus,
      threshold,
      index: BigInt(i),
      share,
    });
  }

  return shares;
}

// Splits a secret into a list of shares with proofs
export function splitSecretWithProofs(
  secret: bigint,
  fieldModulus: bigint,
  threshold: number,
  numberOfShares: number
) {
  const polynomial = generatePolynomialForSecretSharing(
    fieldModulus,
    secret,
    threshold - 1
  );

  const shares: VerifiableSecretShare[] = [];
  for (let i = 1; i <= numberOfShares; i++) {
    const share = evaluatePolynomial(polynomial, BigInt(i));
    shares.push({
      fieldModulus,
      threshold,
      index: BigInt(i),
      share,
      proofs: polynomial.proofs,
    });
  }

  return shares;
}

// Recovers a secret from a list of shares
export function recoverSecret(shares: VerifiableSecretShare[]) {
  if (shares.length < shares[0].threshold) {
    throw new Error("Not enough shares to recover secret");
  }

  let result = 0n;
  for (const share of shares) {
    const coeff = computerLagrangeCoefficients(share.index, shares);
    const item = (share.share * coeff) % shares[0].fieldModulus;

    result = (result + item) % shares[0].fieldModulus;
  }

  return result;
}

// Validates a share of a secret
export function validateShare(share: VerifiableSecretShare) {
  const targetPubkey = secp256k1.ProjectivePoint.fromPrivateKey(
    share.share
  ).toRawBytes(true);

  let resultPubkey = share.proofs[0];

  for (let i = 1; i < share.proofs.length; i++) {
    const pubkey = share.proofs[i];
    const value = share.index ** BigInt(i) % share.fieldModulus;

    const scaledPoint =
      secp256k1.ProjectivePoint.fromHex(pubkey).multiply(value);
    resultPubkey = secp256k1.ProjectivePoint.fromHex(resultPubkey)
      .add(scaledPoint)
      .toRawBytes(true);
  }

  if (!equalBytes(resultPubkey, targetPubkey)) {
    throw new Error("Share is not valid");
  }
}

// Converts a bigint to a private key since imported package doesn't support bigint
export function bigIntToPrivateKey(value: bigint): Uint8Array {
  const hex = value.toString(16).padStart(64, "0");

  const bytes = new Uint8Array(32);
  for (let i = 0; i < 32; i++) {
    bytes[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }

  return bytes;
}
