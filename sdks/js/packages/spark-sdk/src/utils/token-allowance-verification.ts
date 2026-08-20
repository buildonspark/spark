import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { SparkValidationError } from "../errors/types.js";
import type { TokenAllowanceInfo } from "../proto/spark_token.js";
import { TokenAllowanceStatus } from "../proto/spark_token.js";
import {
  hashCreateTokenAllowancePayload,
  hashRevokeTokenAllowancePayload,
} from "./token-allowance-hashing.js";

// Mirrors the SO's ValidateOwnershipSignature: try Schnorr for 64-byte
// signatures, then fall back to ECDSA DER, both against the same key.
function verifyOwnershipSignature(
  signature: Uint8Array,
  hash: Uint8Array,
  publicKey: Uint8Array,
): boolean {
  if (signature.length === 64) {
    try {
      if (schnorr.verify(signature, hash, publicKey.slice(1))) {
        return true;
      }
    } catch {
      // Fall through to DER parsing; rare DER signatures are 64 bytes.
    }
  }
  try {
    const sig = secp256k1.Signature.fromDER(signature);
    // The SO accepts either S representation; low-S would refuse valid records.
    return secp256k1.verify(sig.toCompactRawBytes(), hash, publicKey, {
      lowS: false,
    });
  } catch {
    return false;
  }
}

/**
 * Verifies that a TokenAllowanceInfo returned by query_token_allowances was
 * authored by the owner it names: recomputes the create statement hash from
 * the returned payload and checks owner_signature against the returned owner
 * key, and, for REVOKED records, reconstructs the revoke payload and checks
 * revoke_signature the same way. A queried SO cannot fabricate or alter grant
 * terms without breaking these proofs.
 *
 * Throws SparkValidationError when a proof does not verify.
 */
export function verifyAllowanceRecord(record: TokenAllowanceInfo): void {
  const payload = record.allowancePayload;
  if (!payload) {
    throw new SparkValidationError("allowance record has no payload", {
      field: "allowancePayload",
    });
  }
  const createHash = hashCreateTokenAllowancePayload(payload);
  if (
    !verifyOwnershipSignature(
      record.ownerSignature,
      createHash,
      payload.ownerPublicKey,
    )
  ) {
    throw new SparkValidationError(
      "owner signature does not verify against the returned allowance terms",
      { field: "ownerSignature" },
    );
  }
  if (record.status === TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_REVOKED) {
    const revokeHash = hashRevokeTokenAllowancePayload({
      version: record.revokeVersion,
      allowanceId: payload.allowanceId,
      ownerPublicKey: payload.ownerPublicKey,
      ownerProvidedTimestamp: record.ownerProvidedRevokeTimestamp,
    });
    if (
      !verifyOwnershipSignature(
        record.revokeSignature,
        revokeHash,
        payload.ownerPublicKey,
      )
    ) {
      throw new SparkValidationError(
        "revoke signature does not verify against the returned revocation",
        { field: "revokeSignature" },
      );
    }
  }
}
