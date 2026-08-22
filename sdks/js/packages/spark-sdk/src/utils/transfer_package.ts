import { hexToBytes } from "@noble/curves/utils";
import { type TransferPackage } from "../proto/spark.js";
import { newHasher } from "./hashstructure.js";

// GetTransferPackageSigningPayload returns the signing payload for a transfer package.
// Uses V2 structured hashing with domain tag for collision resistance.
//
// A delegation intent (Spark Pull), when present, is bound too: the cited grant,
// the delegate authorizing the spend, and the exact amount flowing to each
// receiver. Mirrors common.getTransferPackageSigningPayloadV2 in Go field for
// field; the two must agree or a package signed here cannot verify there.
//
// The fields are appended only when an intent is present, so a transfer without
// one hashes exactly as it did before delegation existed. Presence is therefore
// load-bearing: an intent set but empty is a different signed statement from no
// intent at all.
export function getTransferPackageSigningPayload(
  transferID: string,
  transferPackage: TransferPackage,
): Uint8Array {
  const transferIdBytes = hexToBytes(transferID.replaceAll("-", ""));
  const hasher = newHasher(["spark", "transfer", "signing payload"])
    .addBytes(transferIdBytes)
    .addMapStringToBytes(transferPackage.keyTweakPackage);

  const intent = transferPackage.delegationIntent;
  if (intent !== undefined && intent !== null) {
    hasher
      .addString(intent.grantId)
      .addBytes(intent.spenderIdentityPublicKey)
      .addUint64(intent.totalAmountSats)
      .addMapStringToUint64(intent.receiverAmountsSats);
  }
  return hasher.hash();
}

export function getClaimPackageSigningPayload(
  transferID: string,
  keyTweakPackage: Record<string, Uint8Array>,
): Uint8Array {
  const transferIdBytes = hexToBytes(transferID.replaceAll("-", ""));
  return newHasher(["spark", "claim", "signing payload"])
    .addBytes(transferIdBytes)
    .addMapStringToBytes(keyTweakPackage)
    .hash();
}
