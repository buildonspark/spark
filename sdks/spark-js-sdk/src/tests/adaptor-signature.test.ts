import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
  validateOutboundAdaptorSignature,
} from "../utils/adaptor-signature";

describe("adaptor signature", () => {
  it("should validate outbound adaptor signature", () => {
    let failures = 0;

    const msg = "test";
    const hash = sha256(msg);
    for (let i = 0; i < 1000; i++) {
      const privKey = secp256k1.utils.randomPrivateKey();
      const pubkey = schnorr.getPublicKey(privKey);

      const sig = schnorr.sign(hash, privKey);

      expect(schnorr.verify(sig, hash, pubkey)).toBe(true);

      try {
        const { adaptorPrivateKey, adaptorSignature } =
          generateAdaptorFromSignature(sig);

        const adaptorPubkey = secp256k1.getPublicKey(adaptorPrivateKey);
        validateOutboundAdaptorSignature(
          pubkey,
          hash,
          adaptorSignature,
          adaptorPubkey
        );

        const adapterSig = applyAdaptorToSignature(
          pubkey,
          hash,
          adaptorSignature,
          adaptorPrivateKey
        );

        expect(schnorr.verify(adapterSig, hash, pubkey)).toBe(true);
      } catch (e) {
        failures++;
      }
    }

    expect(failures).toBe(0);
  }, 30000);
});
