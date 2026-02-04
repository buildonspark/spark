import { hexToBytes } from "@noble/curves/utils";
import { TransferPackage } from "../proto/spark.js";
import { newHasher } from "./hashstructure.js";

// GetTransferPackageSigningPayload returns the signing payload for a transfer package.
// Uses V2 structured hashing with domain tag for collision resistance.
export function getTransferPackageSigningPayload(
  transferID: string,
  transferPackage: TransferPackage,
): Uint8Array {
  const transferIdBytes = hexToBytes(transferID.replaceAll("-", ""));
  return newHasher(["spark", "transfer", "signing payload"])
    .addBytes(transferIdBytes)
    .addMapStringToBytes(transferPackage.keyTweakPackage)
    .hash();
}
