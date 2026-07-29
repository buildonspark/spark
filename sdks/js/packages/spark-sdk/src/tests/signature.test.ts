import { describe, expect, it } from "@jest/globals";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { hexToBytes } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { type Signature, SignatureScheme } from "../proto/common.js";
import { verifyTypedSignature } from "../utils/signature.js";

// The consuming transfer path is covered only by integration tests, so the
// scheme-dispatch and fail-closed rules are exercised directly here.

const priv = hexToBytes(
  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
);
const compressedPubkey = secp256k1.getPublicKey(priv, true);
const otherPriv = hexToBytes(
  "0000000000000000000000000000000000000000000000000000000000000007",
);
const otherCompressedPubkey = secp256k1.getPublicKey(otherPriv, true);

const digest = sha256(new TextEncoder().encode("leaf-id:transfer-id:cipher"));

function ecdsaCompact(d: Uint8Array, key = priv): Uint8Array {
  return secp256k1.sign(d, key).toBytes("compact");
}
function ecdsaDer(d: Uint8Array, key = priv): Uint8Array {
  return secp256k1.sign(d, key).toBytes("der");
}
function schnorrSig(d: Uint8Array, key = priv): Uint8Array {
  return schnorr.sign(d, key);
}
function makeTypedSig(
  scheme: SignatureScheme,
  signature: Uint8Array,
): Signature {
  return { scheme, signature };
}

describe("verifyTypedSignature", () => {
  it("accepts a legacy compact ECDSA signature (what JS senders emit)", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        legacy: ecdsaCompact(digest),
      }),
    ).toBe(true);
  });

  it("accepts a legacy DER ECDSA signature (what Go senders emit)", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        legacy: ecdsaDer(digest),
      }),
    ).toBe(true);
  });

  it("rejects a legacy ECDSA signature from the wrong key", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        legacy: ecdsaCompact(digest, otherPriv),
      }),
    ).toBe(false);
  });

  it("accepts a typed ECDSA signature in strict DER", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_ECDSA,
          ecdsaDer(digest),
        ),
      }),
    ).toBe(true);
  });

  it("rejects a typed ECDSA signature in compact encoding (DER only)", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_ECDSA,
          ecdsaCompact(digest),
        ),
      }),
    ).toBe(false);
  });

  it("accepts a typed Schnorr (BIP-340) signature against a compressed key", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_SCHNORR,
          schnorrSig(digest),
        ),
      }),
    ).toBe(true);
  });

  it("rejects an ECDSA compact signature mislabeled as Schnorr", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_SCHNORR,
          ecdsaCompact(digest),
        ),
      }),
    ).toBe(false);
  });

  it("rejects a Schnorr signature mislabeled as ECDSA", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_ECDSA,
          schnorrSig(digest),
        ),
      }),
    ).toBe(false);
  });

  it("rejects when both legacy and typed signatures are present", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        legacy: ecdsaCompact(digest),
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_ECDSA,
          ecdsaDer(digest),
        ),
      }),
    ).toBe(false);
  });

  it("rejects an UNSPECIFIED scheme even with otherwise-valid bytes", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_UNSPECIFIED,
          ecdsaDer(digest),
        ),
      }),
    ).toBe(false);
  });

  it("rejects an unrecognized scheme value", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: makeTypedSig(99 as SignatureScheme, ecdsaDer(digest)),
      }),
    ).toBe(false);
  });

  it("rejects when neither signature is present", () => {
    expect(verifyTypedSignature({ digest, compressedPubkey })).toBe(false);
  });

  it("rejects a valid signature over a different digest", () => {
    const otherDigest = sha256(new TextEncoder().encode("tampered"));
    expect(
      verifyTypedSignature({
        digest: otherDigest,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_SCHNORR,
          schnorrSig(digest),
        ),
      }),
    ).toBe(false);
  });

  it("does not throw on malformed signature bytes", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_ECDSA,
          new Uint8Array([1, 2, 3]),
        ),
      }),
    ).toBe(false);
  });

  it("rejects a verification against the wrong key for Schnorr", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey: otherCompressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_SCHNORR,
          schnorrSig(digest),
        ),
      }),
    ).toBe(false);
  });

  it("does not throw on a structurally malformed typed signature (no bytes)", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        typed: { scheme: SignatureScheme.SIGNATURE_SCHEME_ECDSA } as Signature,
      }),
    ).toBe(false);
  });

  it("rejects a legacy signature accompanied by a typed object without bytes", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey,
        legacy: ecdsaCompact(digest),
        typed: { scheme: SignatureScheme.SIGNATURE_SCHEME_ECDSA } as Signature,
      }),
    ).toBe(false);
  });

  it("rejects a 65-byte uncompressed key on the typed ECDSA path", () => {
    // Noble's ECDSA verify accepts uncompressed keys, so without the length
    // guard this otherwise-valid signature would verify.
    const uncompressedPubkey = secp256k1.getPublicKey(priv, false);
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey: uncompressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_ECDSA,
          ecdsaDer(digest),
        ),
      }),
    ).toBe(false);
  });

  it("rejects a 65-byte uncompressed key on the legacy ECDSA path", () => {
    const uncompressedPubkey = secp256k1.getPublicKey(priv, false);
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey: uncompressedPubkey,
        legacy: ecdsaCompact(digest),
      }),
    ).toBe(false);
  });

  it("rejects a 32-byte x-only key on the Schnorr path (33-byte compressed required)", () => {
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey: compressedPubkey.subarray(1),
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_SCHNORR,
          schnorrSig(digest),
        ),
      }),
    ).toBe(false);
  });

  it("rejects a 33-byte key with an invalid compression prefix on the Schnorr path", () => {
    const badPrefixKey = compressedPubkey.slice();
    badPrefixKey[0] = 0x04;
    expect(
      verifyTypedSignature({
        digest,
        compressedPubkey: badPrefixKey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_SCHNORR,
          schnorrSig(digest),
        ),
      }),
    ).toBe(false);
  });

  it("rejects a non-32-byte digest even when the signature covers it", () => {
    // BIP-340 signs arbitrary-length messages, so without the length guard a
    // signature over raw un-hashed data would verify.
    const rawMessage = new TextEncoder().encode("raw un-hashed payload");
    expect(
      verifyTypedSignature({
        digest: rawMessage,
        compressedPubkey,
        typed: makeTypedSig(
          SignatureScheme.SIGNATURE_SCHEME_SCHNORR,
          schnorrSig(rawMessage),
        ),
      }),
    ).toBe(false);
  });
});
