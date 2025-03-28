import { numberToBytesBE } from "@noble/curves/abstract/utils";
import { Network } from "../proto/spark.js";
import { hashTokenTransaction } from "../utils/token-hashing.js";

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
      network: Network.REGTEST,
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
          withdrawBondSats: 10000,
          withdrawLocktime: 100,
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
          revocationPublicKey: new Uint8Array(0),
        },
      ],
      sparkOperatorIdentityPublicKeys: [],
    };

    const hash = hashTokenTransaction(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      244, 209, 163, 169, 50, 197, 66, 157, 227, 253, 169, 128, 250, 9, 80, 147,
      75, 205, 179, 245, 156, 48, 34, 211, 67, 83, 84, 28, 56, 139, 134, 125
    ]);
  });
});
