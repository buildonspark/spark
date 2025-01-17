import { modInverse } from "../utils/secret-sharing";

describe("Secret Sharing", () => {
  describe("modInverse", () => {
    it("should correctly calculate modular multiplicative inverse", () => {
      // Test cases: [a, m, expected]
      const testCases: [bigint, bigint, bigint][] = [
        [3n, 11n, 4n], // 3 * 4 ≡ 1 (mod 11)
        [10n, 17n, 12n], // 10 * 12 ≡ 1 (mod 17)
        [7n, 13n, 2n], // 7 * 2 ≡ 1 (mod 13)
      ];

      for (const [a, m, expected] of testCases) {
        const result = modInverse(a, m);
        expect(result).toBe(expected);
        // Verify that (a * result) % m === 1
        expect((a * result) % m).toBe(1n);
      }
    });

    it("should throw error when modular inverse doesn't exist", () => {
      expect(() => modInverse(4n, 8n)).toThrow(
        "Modular inverse does not exist"
      );
      expect(() => modInverse(6n, 9n)).toThrow(
        "Modular inverse does not exist"
      );
    });
  });
});
