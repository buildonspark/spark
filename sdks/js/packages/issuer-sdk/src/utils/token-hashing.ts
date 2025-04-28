import { sha256 } from "@scure/btc-signer/utils";
import { FreezeTokensPayload } from "@buildonspark/spark-sdk/proto/spark";
import { ValidationError } from "@buildonspark/spark-sdk";

export function hashFreezeTokensPayload(
  payload: FreezeTokensPayload,
): Uint8Array {
  if (!payload) {
    throw new ValidationError("Freeze tokens payload cannot be nil", {
      field: "payload",
      value: payload,
      expected: "valid freeze tokens payload",
    });
  }

  let allHashes: Uint8Array[] = [];

  // Hash owner public key
  const ownerPubKeyHash = sha256.create();
  if (payload.ownerPublicKey) {
    ownerPubKeyHash.update(payload.ownerPublicKey);
  }
  allHashes.push(ownerPubKeyHash.digest());

  // Hash token public key
  const tokenPubKeyHash = sha256.create();
  if (payload.tokenPublicKey) {
    tokenPubKeyHash.update(payload.tokenPublicKey);
  }
  allHashes.push(tokenPubKeyHash.digest());

  // Hash shouldUnfreeze
  const shouldUnfreezeHash = sha256.create();
  shouldUnfreezeHash.update(new Uint8Array([payload.shouldUnfreeze ? 1 : 0]));
  allHashes.push(shouldUnfreezeHash.digest());

  // Hash timestamp
  const timestampHash = sha256.create();
  if (payload.issuerProvidedTimestamp) {
    const timestampBytes = new Uint8Array(8);
    new DataView(timestampBytes.buffer).setBigUint64(
      0,
      BigInt(payload.issuerProvidedTimestamp),
      true, // true for little-endian
    );
    timestampHash.update(timestampBytes);
  }
  allHashes.push(timestampHash.digest());

  // Hash operator identity public key
  const operatorPubKeyHash = sha256.create();
  if (payload.operatorIdentityPublicKey) {
    operatorPubKeyHash.update(payload.operatorIdentityPublicKey);
  }
  allHashes.push(operatorPubKeyHash.digest());

  // Final hash of all concatenated hashes
  const finalHash = sha256.create();
  for (const hash of allHashes) {
    finalHash.update(hash);
  }
  return finalHash.digest();
}
