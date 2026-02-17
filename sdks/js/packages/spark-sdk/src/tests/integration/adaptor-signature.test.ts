import { describe, expect, it } from "@jest/globals";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@noble/hashes/sha2";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
  validateOutboundAdaptorSignature,
} from "../../utils/adaptor-signature.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";

describe("adaptor signature", () => {
  it("should validate outbound adaptor signature", async () => {
    let failures = 0;

    const { wallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    const msg = "test";
    const hash = sha256(msg);
    for (let i = 0; i < 1000; i++) {
      const privateKey = secp256k1.utils.randomPrivateKey();
      const schnorrPublicKey = schnorr.getPublicKey(privateKey);
      const signature = schnorr.sign(hash, privateKey);

      expect(schnorr.verify(signature, hash, schnorrPublicKey)).toBe(true);

      try {
        const { adaptorPrivateKey, adaptorSignature } =
          generateAdaptorFromSignature(signature);

        const adaptorPubkey = secp256k1.getPublicKey(adaptorPrivateKey);
        validateOutboundAdaptorSignature(
          schnorrPublicKey,
          hash,
          adaptorSignature,
          adaptorPubkey,
        );

        const adapterSig = applyAdaptorToSignature(
          schnorrPublicKey,
          hash,
          adaptorSignature,
          adaptorPrivateKey,
        );

        expect(schnorr.verify(adapterSig, hash, schnorrPublicKey)).toBe(true);
      } catch (e) {
        failures++;
      }
    }

    expect(failures).toBe(0);
  }, 30000);
});
