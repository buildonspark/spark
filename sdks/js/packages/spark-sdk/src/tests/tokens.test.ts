import { numberToBytesBE } from "@noble/curves/abstract/utils";
import { hashTokenTransaction } from "../utils/token-hashing.js";
import { Network } from "../proto/spark.js";

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
      tokenInputs: {
        $case: "mintInput" as const,
        mintInput: {
          issuerPublicKey: tokenPublicKey,
          issuerProvidedTimestamp: 100,
        },
      },
      tokenOutputs: [
        {
          id: "db1a4e48-0fc5-4f6c-8a80-d9d6c561a436",
          ownerPublicKey: identityPubKey,
          withdrawBondSats: 10000,
          withdrawRelativeBlockLocktime: 100,
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
          revocationCommitment: identityPubKey,
        },
      ],
      sparkOperatorIdentityPublicKeys: [],
      network: Network.REGTEST,
    };

    const hash = hashTokenTransaction(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      66, 235, 134, 101, 172, 110, 147, 77, 122, 48, 86, 240, 239, 9, 163, 82,
      120, 234, 246, 206, 245, 242, 186, 180, 154, 41, 207, 179, 194, 31, 211,
      36,
    ]);
  });
});
