import { describe, expect, it } from "@jest/globals";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import {
  applyAdaptorToSignature,
  generateAdaptorFromSignature,
  validateOutboundAdaptorSignature,
} from "../utils/adaptor-signature.js";
import { Network } from "../utils/network.js";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";

describe("adaptor signature", () => {
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "should validate outbound adaptor signature",
    async () => {
      let failures = 0;

      const wallet = new SparkWalletTesting(Network.LOCAL);
      await wallet.initWalletFromMnemonic();

      const msg = "test";
      const hash = sha256(msg);
      for (let i = 0; i < 1000; i++) {
        const pubKey = await wallet.getSigner().generatePublicKey();
        const pubkey = await wallet.getSigner().getSchnorrPublicKey(pubKey);

        const sig = await wallet.getSigner().signSchnorr(hash, pubKey);

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
    },
    30000
  );
});
