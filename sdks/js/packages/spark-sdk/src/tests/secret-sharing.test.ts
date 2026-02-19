import { beforeAll, describe, expect, it } from "@jest/globals";
import { numberToBytesBE } from "@noble/curves/utils";
import { setSparkFrostOnce } from "../spark-bindings/spark-bindings.js";
import { SparkFrost } from "../spark-bindings/spark-bindings.node.js";
import {
  recoverSecret,
  splitSecretWithProofs,
  validateShare,
} from "../utils/secret-sharing.js";

beforeAll(() => {
  setSparkFrostOnce(new SparkFrost());
});

describe("Secret Sharing", () => {
  it("test secret sharing", async () => {
    const secret =
      56223216183876340914672117764605975762373003965917245943571257601961255596156n;
    const secretBytes = numberToBytesBE(secret, 32);
    const threshold = 3;
    const numberOfShares = 5;

    const shares = await splitSecretWithProofs(
      secretBytes,
      threshold,
      numberOfShares,
    );

    expect(shares).toHaveLength(numberOfShares);

    for (const share of shares) {
      await validateShare(share);
    }

    const recoveredSecretBytes = await recoverSecret(
      shares.slice(0, threshold),
    );
    expect(new Uint8Array(recoveredSecretBytes)).toEqual(
      new Uint8Array(secretBytes),
    );
  });

  it("should reject shares with invalid proof length", async () => {
    const secret =
      56223216183876340914672117764605975762373003965917245943571257601961255596156n;
    const secretBytes = numberToBytesBE(secret, 32);
    const threshold = 3;
    const numberOfShares = 5;

    const shares = await splitSecretWithProofs(
      secretBytes,
      threshold,
      numberOfShares,
    );

    expect(shares.length).toBeGreaterThan(0);
    const validShare = shares[0]!;
    const firstProof = validShare.proofs[0];
    expect(firstProof).toBeDefined();

    const invalidShare = {
      threshold: validShare.threshold,
      index: validShare.index,
      share: validShare.share,
      proofs: [...validShare.proofs, firstProof!],
    };

    await expect(validateShare(invalidShare)).rejects.toBeDefined();
  });
});
