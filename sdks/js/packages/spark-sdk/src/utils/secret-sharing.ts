import { bytesToHex, equalBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { ValidationError } from "../errors/index.js";
import { getCrypto } from "./crypto.js";

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

export function getRandomBigInt(max: bigint): bigint {
  const byteLength = (max.toString(2).length + 7) >> 3;
  const maxBigInt = max;

  const mask = (1n << BigInt(max.toString(2).length)) - 1n;
  while (true) {
    const crypto = getCrypto();
    const randBytes = crypto.getRandomValues(new Uint8Array(byteLength + 1));

    const randValue = BigInt("0x" + bytesToHex(randBytes)) & mask;

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
    throw new ValidationError("Modular inverse does not exist", {
      field: "modInverse",
      value: `a: ${a}, m: ${m}`,
      expected: "a and m must be coprime",
    });
  }

  return ((old_s % m) + m) % m;
}

// Evaluates a polynomial at a given point
export function evaluatePolynomial(polynomial: Polynomial, x: bigint): bigint {
  let result = 0n;
  for (let i = 0; i < polynomial.coefficients.length; i++) {
    const coeff = polynomial.coefficients[i];
    if (!coeff) {
      throw new ValidationError("Coefficient is undefined", {
        field: "coefficient",
        value: "undefined",
        expected: "A valid bigint coefficient",
      });
    }

    const xPow = x ** BigInt(i) % polynomial.fieldModulus;

    result = (result + xPow * coeff) % polynomial.fieldModulus;
  }
  return result;
}

// Divides two numbers in a given field modulus
export function fieldDiv(
  numerator: bigint,
  denominator: bigint,
  fieldModulus: bigint,
): bigint {
  if (denominator === 0n) {
    throw new ValidationError("Division by zero", {
      field: "denominator",
      value: "0",
      expected: "Non-zero value",
    });
  }

  const inverse = modInverse(denominator, fieldModulus);
  return (numerator * inverse) % fieldModulus;
}

// Computes the Lagrange coefficient for a given index and a set of points
export function computerLagrangeCoefficients(
  index: bigint,
  points: SecretShare[],
) {
  let numerator = 1n;
  let denominator = 1n;
  let fieldModulus = points[0]?.fieldModulus;
  if (!fieldModulus) {
    throw new ValidationError("Field modulus is undefined", {
      field: "fieldModulus",
      value: "undefined",
      expected: "A valid field modulus",
    });
  }

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
  degree: number,
): Polynomial {
  const coefficients: bigint[] = new Array(degree);
  const proofs: Uint8Array[] = new Array(degree);

  coefficients[0] = secret;
  proofs[0] = secp256k1.Point.fromPrivateKey(secret).toBytes(true);

  for (let i = 1; i < degree; i++) {
    const coefficient = getRandomBigInt(fieldModulus);
    coefficients[i] = coefficient;
    proofs[i] = secp256k1.Point.fromPrivateKey(coefficient).toBytes(true);
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
  numberOfShares: number,
) {
  const polynomial = generatePolynomialForSecretSharing(
    fieldModulus,
    secret,
    threshold,
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
  numberOfShares: number,
) {
  const polynomial = generatePolynomialForSecretSharing(
    fieldModulus,
    secret,
    threshold,
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
  if (shares.length === 0) return 0n;

  const threshold = shares[0]?.threshold;
  const fieldModulus = shares[0]?.fieldModulus;

  if (!threshold || !fieldModulus) {
    throw new ValidationError("Shares are not valid", {
      field: "shares",
      value: "Missing threshold or fieldModulus",
      expected: "Valid shares with threshold and fieldModulus",
    });
  }

  if (shares.length < threshold) {
    throw new ValidationError("Not enough shares to recover secret", {
      field: "shares",
      value: shares.length,
      expected: `At least ${threshold} shares`,
    });
  }

  let result = 0n;
  for (const share of shares) {
    const coeff = computerLagrangeCoefficients(share.index, shares);
    const item = (share.share * coeff) % fieldModulus;

    result = (result + item) % fieldModulus;
  }

  return result;
}

// Validates a share of a secret
export function validateShare(share: VerifiableSecretShare) {
  const targetPubkey = secp256k1.Point.fromPrivateKey(share.share).toBytes(
    true,
  );

  let resultPubkey = share.proofs[0];
  if (!resultPubkey) {
    throw new ValidationError("Result pubkey is not valid", {
      field: "resultPubkey",
      value: "null",
      expected: "Valid public key bytes",
    });
  }

  for (let i = 1; i < share.proofs.length; i++) {
    const pubkey = share.proofs[i];
    if (!pubkey) {
      throw new ValidationError("Pubkey is not valid", {
        field: "pubkey",
        value: "null",
        expected: "Valid public key bytes",
      });
    }
    const value = share.index ** BigInt(i) % share.fieldModulus;

    const scaledPoint = secp256k1.Point.fromHex(pubkey).multiply(value);
    resultPubkey = secp256k1.Point.fromHex(resultPubkey)
      .add(scaledPoint)
      .toBytes(true);
  }

  if (!equalBytes(resultPubkey, targetPubkey)) {
    throw new ValidationError("Share is not valid", {
      field: "share",
      value: "Invalid proof",
      expected: "Valid share with matching proofs",
    });
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
