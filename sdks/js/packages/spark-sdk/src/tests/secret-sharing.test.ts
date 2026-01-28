import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  modInverse,
  recoverSecret,
  splitSecretWithProofs,
  validateShare,
} from "../utils/secret-sharing.js";

describe("Secret Sharing", () => {
  describe("modInverse", () => {
    it("should correctly calculate modular multiplicative inverse", () => {
      // Test cases: [a, m, expected]
      const testCases: [bigint, bigint, bigint][] = [
        [3n, 11n, 4n], // 3 * 4 ≡ 1 (mod 11)
        [10n, 17n, 12n], // 10 * 12 ≡ 1 (mod 17)
        [7n, 13n, 2n], // 7 * 2 ≡ 1 (mod 13)
        [-1n, secp256k1.CURVE.n, secp256k1.CURVE.n - 1n],
      ];

      for (const [a, m, expected] of testCases) {
        const result = modInverse(a, m);
        expect(result).toBe(expected);

        // Normalize the result of (a * result) before taking modulo
        const product = a * result;
        const normalizedProduct = ((product % m) + m) % m;
        expect(normalizedProduct).toBe(1n);
      }
    });

    it("should throw error when modular inverse doesn't exist", () => {
      expect(() => modInverse(4n, 8n)).toThrow(
        "Modular inverse does not exist",
      );
      expect(() => modInverse(6n, 9n)).toThrow(
        "Modular inverse does not exist",
      );
    });
  });

  it("test secret sharing", () => {
    const fieldModulus = secp256k1.CURVE.n;
    const secret =
      56223216183876340914672117764605975762373003965917245943571257601961255596156n;
    const threshold = 3;
    const numberOfShares = 5;

    const shares = splitSecretWithProofs(
      secret,
      fieldModulus,
      threshold,
      numberOfShares,
    );

    for (const share of shares) {
      validateShare(share);
    }

    const recoveredSecret = recoverSecret(shares.slice(0, threshold));
    expect(recoveredSecret).toBe(secret);
  });
});
