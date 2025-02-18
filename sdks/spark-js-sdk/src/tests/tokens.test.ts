import { numberToBytesBE } from "@noble/curves/abstract/utils";
import {
  hashOperatorSpecificTokenTransactionSignablePayload,
  hashTokenTransaction,
} from "../utils/tokens";

describe("hash token transaction", () => {
  it("should produce the exact same hash", () => {
    const tokenAmount: bigint = 1000n;

    const tokenPublicKey = new Uint8Array([
      242, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
      252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 45,
    ]);

    const identityPubKey = new Uint8Array([
      25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
      252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
    ]);

    const tokenTransaction = {
      tokenInput: {
        $case: "mintInput" as const,
        mintInput: {
          issuerPublicKey: tokenPublicKey,
          issuerProvidedTimestamp: 100,
        },
      },
      outputLeaves: [
        {
          id: "db1a4e48-0fc5-4f6c-8a80-d9d6c561a436",
          ownerPublicKey: identityPubKey,
          withdrawalBondSats: 10000,
          withdrawalLocktime: 100,
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
          revocationPublicKey: new Uint8Array(0),
        },
      ],
      sparkOperatorIdentityPublicKeys: [],
    };

    const hash = hashTokenTransaction(tokenTransaction, false);

    console.log("Hash: ", hash);

    expect(Array.from(hash)).toEqual([
      142, 183, 62, 229, 88, 150, 67, 230, 159, 27, 221, 120, 221, 0, 49, 134,
      139, 11, 249, 227, 197, 145, 122, 52, 136, 189, 98, 172, 53, 238, 118, 72,
    ]);
  });

  it("should produce the exact same hash part 2", () => {
    const tokenAmount: bigint = 1000n;

    const tokenPublicKey = new Uint8Array([
      242, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
      252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 45,
    ]);

    const identityPubKey = new Uint8Array([
      25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
      252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
    ]);

    const tokenTransaction = {
      tokenInput: {
        $case: "mintInput" as const,
        mintInput: {
          issuerPublicKey: tokenPublicKey,
          issuerProvidedTimestamp: 100,
        },
      },
      outputLeaves: [
        {
          id: "db1a4e48-0fc5-4f6c-8a80-d9d6c561a436",
          ownerPublicKey: identityPubKey,
          withdrawalBondSats: 10000,
          withdrawalLocktime: 100,
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
          revocationPublicKey: new Uint8Array(0),
        },
      ],
      sparkOperatorIdentityPublicKeys: [
        new Uint8Array([
          25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123,
          50, 252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159,
          173, 46,
        ]),
        new Uint8Array([
          25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123,
          50, 252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 156,
          173, 46,
        ]),
      ],
    };

    const hash = hashTokenTransaction(tokenTransaction, false);

    console.log("Hash: ", hash);

    expect(Array.from(hash)).toEqual([
      118, 30, 129, 21, 204, 132, 45, 226, 127, 114, 43, 4, 153, 100, 234, 235,
      220, 121, 8, 145, 143, 219, 168, 222, 31, 159, 175, 121, 78, 87, 134, 87,
    ]);
  });
});

describe("hash operator specific token transaction signable payload", () => {
  it("should produce consistent hashes", () => {
    // Test case 1: Both fields present
    const testCase1 = {
      finalTokenTransactionHash: new Uint8Array([1, 2, 3, 4]),
      operatorIdentityPublicKey: new Uint8Array([5, 6, 7, 8]),
    };
    const hash1 =
      hashOperatorSpecificTokenTransactionSignablePayload(testCase1);

    // Test case 2: Empty fields
    const testCase2 = {
      finalTokenTransactionHash: new Uint8Array(0),
      operatorIdentityPublicKey: new Uint8Array(0),
    };
    const hash2 =
      hashOperatorSpecificTokenTransactionSignablePayload(testCase2);

    // Test case 3: One field empty
    const testCase3 = {
      finalTokenTransactionHash: new Uint8Array([1, 2, 3, 4]),
      operatorIdentityPublicKey: new Uint8Array(0),
    };
    const hash3 =
      hashOperatorSpecificTokenTransactionSignablePayload(testCase3);
  });

  it("should produce the exact same hash", () => {
    const sampleTokenHash = new Uint8Array([0, 1]);
    const samplePublicKey = new Uint8Array([1, 0]);

    const payload = {
      finalTokenTransactionHash: sampleTokenHash,
      operatorIdentityPublicKey: samplePublicKey,
    };

    const hash = hashOperatorSpecificTokenTransactionSignablePayload(payload);

    expect(Array.from(hash)).toEqual([
      24, 134, 89, 30, 173, 70, 40, 117, 169, 133, 82, 81, 219, 74, 39, 126,
      186, 174, 255, 170, 79, 215, 9, 111, 63, 8, 15, 123, 212, 247, 134, 70,
    ]);
  });
});
