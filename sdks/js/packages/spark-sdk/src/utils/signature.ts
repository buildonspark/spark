import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { type Signature, SignatureScheme } from "../proto/common.js";

/**
 * Verifies a per-leaf-style authorization signature that may be either a legacy
 * bare-bytes ECDSA signature or a scheme-tagged {@link Signature}.
 *
 * The legacy `signature` field and the typed `typedSignature` field are mutually
 * exclusive on the wire: the sender populates exactly one. This is the single
 * place that decides which scheme to verify, so all the rules live here:
 *  - both present        -> reject. Conformant protobuf decoding enforces the
 *    oneof (last field wins), so this cannot arrive from decoded wire data;
 *    it is defense-in-depth for direct in-process callers.
 *  - typed present       -> dispatch on scheme; UNSPECIFIED/unknown -> reject
 *  - only legacy present -> ECDSA (today's behavior)
 *  - neither present     -> reject
 *
 * `digest` is the already-computed 32-byte message hash (the same value the
 * signer signed); any other length is rejected. `compressedPubkey` is the
 * 33-byte compressed signer key; any other length (e.g. a 65-byte uncompressed
 * key) is rejected for every scheme. BIP-340 Schnorr is verified against its
 * 32-byte x-only form (the compressed key minus its leading parity byte),
 * matching how the signer produced it.
 *
 * Encodings: a typed ECDSA signature must be strict DER (the canonical form
 * for identity signatures protocol-wide, per the scheme's contract in
 * common.proto). The legacy field predates that contract and carries both
 * encodings on the wire (JS senders emit compact, Go senders DER), so it is
 * parsed leniently.
 */
export function verifyTypedSignature(args: {
  digest: Uint8Array;
  compressedPubkey: Uint8Array;
  legacy?: Uint8Array;
  typed?: Signature;
}): boolean {
  const { digest, compressedPubkey, legacy, typed } = args;
  // Enforce the compressed-key contract for every scheme up front: noble's
  // ECDSA verify would otherwise also accept a valid 65-byte uncompressed key.
  if (digest.length !== 32 || compressedPubkey.length !== 33) {
    return false;
  }
  const hasLegacy = legacy != null && legacy.length > 0;

  // Mutually exclusive: refuse to guess if both arrived, rather than silently
  // verifying one and ignoring the other (downgrade risk). Any typed object at
  // all conflicts with a legacy signature, even one without signature bytes.
  if (hasLegacy && typed != null) {
    return false;
  }

  try {
    if (typed != null) {
      if ((typed.signature?.length ?? 0) === 0) {
        return false;
      }
      switch (typed.scheme) {
        case SignatureScheme.SIGNATURE_SCHEME_ECDSA:
          return secp256k1.verify(typed.signature, digest, compressedPubkey, {
            format: "der",
          });
        case SignatureScheme.SIGNATURE_SCHEME_SCHNORR:
          // The prefix must be checked explicitly here: the parity byte is
          // stripped rather than parsed, so unlike the ECDSA paths an invalid
          // prefix would never reach noble's point decoding.
          if (compressedPubkey[0] !== 0x02 && compressedPubkey[0] !== 0x03) {
            return false;
          }
          return schnorr.verify(
            typed.signature,
            digest,
            compressedPubkey.subarray(1),
          );
        default:
          // UNSPECIFIED / UNRECOGNIZED: a typed signature must name a scheme we
          // understand. Fail closed.
          return false;
      }
    }

    if (hasLegacy) {
      return secp256k1.verify(legacy, digest, compressedPubkey);
    }
  } catch {
    // Malformed signature/key bytes are an invalid signature, not a crash.
    return false;
  }

  return false;
}
