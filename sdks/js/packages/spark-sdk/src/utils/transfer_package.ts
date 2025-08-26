import { hexToBytes } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { TransferPackage } from "../proto/spark.js";

// GetTransferPackageSigningPayload returns the signing payload for a transfer package.
// The payload is a hash of the transfer ID and the encrypted payload sorted by key.
export function getTransferPackageSigningPayload(
  transferID: string,
  transferPackage: TransferPackage,
): Uint8Array {
  const encryptedPayload = transferPackage.keyTweakPackage;

  // convert map to array of key-value pairs
  const pairs: { key: string; value: Uint8Array }[] = Object.entries(
    encryptedPayload,
  ).map(([key, value]) => ({ key, value }));

  // Sort the slice by key to ensure deterministic ordering
  // This is important for consistent signing payloads
  pairs.sort((a, b) => a.key.localeCompare(b.key));

  const encoder = new TextEncoder();
  let message = hexToBytes(transferID.replaceAll("-", ""));

  for (const pair of pairs) {
    const keyPart = encoder.encode(pair.key + ":");
    const separator = encoder.encode(";");

    message = new Uint8Array([
      ...message,
      ...keyPart,
      ...pair.value,
      ...separator,
    ]);
  }

  return sha256(message);
}
