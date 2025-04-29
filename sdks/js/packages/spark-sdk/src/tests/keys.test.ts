import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  addPrivateKeys,
  addPublicKeys,
  applyAdditiveTweakToPublicKey,
  lastKeyWithTarget,
  subtractPrivateKeys,
  subtractPublicKeys,
  sumOfPrivateKeys,
} from "../utils/keys.js";

describe("keys", () => {
  it("test key addition", () => {
    const privKeyA = secp256k1.utils.randomPrivateKey();
    const privKeyB = secp256k1.utils.randomPrivateKey();

    const pubKeyA = secp256k1.getPublicKey(privKeyA, true);
    const pubKeyB = secp256k1.getPublicKey(privKeyB, true);

    const privSum = addPrivateKeys(privKeyA, privKeyB);
    const pubSum = addPublicKeys(pubKeyA, pubKeyB);

    const target = secp256k1.getPublicKey(privSum, true);
    expect(target).toStrictEqual(pubSum);
  });

  it("test key subtraction", () => {
    const privKeyA = secp256k1.utils.randomPrivateKey();
    const privKeyB = secp256k1.utils.randomPrivateKey();

    const pubKeyA = secp256k1.getPublicKey(privKeyA, true);
    const pubKeyB = secp256k1.getPublicKey(privKeyB, true);

    const privDiff = subtractPrivateKeys(privKeyA, privKeyB);
    const pubDiff = subtractPublicKeys(pubKeyA, pubKeyB);

    const target = secp256k1.getPublicKey(privDiff, true);
    expect(target).toStrictEqual(pubDiff);
  });

  it("test sum of private keys", () => {
    const keys = Array.from({ length: 10 }, () =>
      secp256k1.utils.randomPrivateKey(),
    );
    const sum = sumOfPrivateKeys(keys);
    let sum2 = keys[0];
    for (let i = 1; i < keys.length; i++) {
      sum2 = addPrivateKeys(sum2!, keys[i]!);
    }
    expect(sum).toStrictEqual(sum2);
  });

  it("test private key tweak with target", () => {
    const privKey = secp256k1.utils.randomPrivateKey();
    const keys = Array.from({ length: 10 }, () =>
      secp256k1.utils.randomPrivateKey(),
    );

    const tweak = lastKeyWithTarget(privKey, keys);

    keys.push(tweak);

    const sum = sumOfPrivateKeys(keys);

    expect(sum).toStrictEqual(privKey);
  });

  it("test apply additive tweak to public key", () => {
    const privKey = secp256k1.utils.randomPrivateKey();
    const pubKey = secp256k1.getPublicKey(privKey, true);

    const tweak = secp256k1.utils.randomPrivateKey();

    const newPrivKey = addPrivateKeys(privKey, tweak);
    const target = secp256k1.getPublicKey(newPrivKey, true);

    const newPubKey = applyAdditiveTweakToPublicKey(pubKey, tweak);

    expect(newPubKey).toStrictEqual(target);
  });
});
