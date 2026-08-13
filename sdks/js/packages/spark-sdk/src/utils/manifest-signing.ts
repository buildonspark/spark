import { secp256k1 } from "@noble/curves/secp256k1";
import type { TransferManifest } from "../proto/spark.js";
import {
  hashSerializedTransferManifest,
  hashTransferManifest,
} from "./manifest-hashing.js";

/**
 * The sender's identity-key ECDSA signature over `manifest_hash`.
 *
 * This is the existing transfer-package signing scheme
 * (`getTransferPackageSigningPayload` -> `signMessageWithIdentityKey`) applied to
 * the manifest digest — not a new scheme, so it cannot diverge from what the
 * SO/SSP verify for the User-Created flows (send / coop-exit). The digest itself
 * is the shared Rust core's `manifest_hash`, already cross-language. The signing
 * layer (sign -> verify, strict-DER + low-S) is exercised over the shared
 * `spark/testdata/transfer_manifest_hash_cases.json` shapes; signatures are
 * verify-checked, not byte-pinned — ECDSA is nonce-dependent, so verification,
 * not byte-equality, is the contract.
 */

/** The subset of SparkSigner this module needs — a raw identity-key signer. */
export interface ManifestSigner {
  /**
   * Sign the raw 32-byte digest (no additional hashing) and return a **low-S,
   * DER-encoded** ECDSA signature. This is a hard requirement, not a preference:
   * the SO's verifier (and `verifyTransferManifestSignature`) reject high-S or
   * non-DER as non-canonical. SparkSigner's default output satisfies it; a custom
   * signer MUST normalize to low-S itself — `signTransferManifest` forwards the
   * bytes as-is and does not fix them up.
   */
  signMessageWithIdentityKey(message: Uint8Array): Promise<Uint8Array>;
}

export async function signTransferManifest(
  manifest: TransferManifest,
  signer: ManifestSigner,
): Promise<Uint8Array> {
  return signer.signMessageWithIdentityKey(
    await hashTransferManifest(manifest),
  );
}

/**
 * Countersign a manifest received as bytes. The peer's copy is what the
 * operators bind, so the attestation is taken over it directly.
 */
export async function signSerializedTransferManifest(
  manifestBytes: Uint8Array,
  signer: ManifestSigner,
): Promise<Uint8Array> {
  return signer.signMessageWithIdentityKey(
    await hashSerializedTransferManifest(manifestBytes),
  );
}

export async function verifyTransferManifestSignature(
  manifest: TransferManifest,
  signature: Uint8Array,
  identityPublicKey: Uint8Array,
): Promise<boolean> {
  const hash = await hashTransferManifest(manifest);
  // A malformed identity key is a caller bug and throws — the fail-closed catch
  // below covers only the signature bytes, which may be echoed by a peer.
  secp256k1.ProjectivePoint.fromHex(identityPublicKey);
  // DER-only, matching the SO's strict VerifyECDSASignature: a signature this
  // helper blesses must be exactly what the server accepts, so compact or
  // otherwise unparseable bytes verify false, never throw.
  try {
    const compact = secp256k1.Signature.fromDER(signature).toCompactRawBytes();
    // format is pinned so noble can't autodetect compact bytes that happen to
    // be DER-shaped as a different signature; lowS is pinned (not left to
    // noble's default) because the SO rejects high-S as non-canonical.
    return secp256k1.verify(compact, hash, identityPublicKey, {
      format: "compact",
      lowS: true,
    });
  } catch {
    return false;
  }
}
