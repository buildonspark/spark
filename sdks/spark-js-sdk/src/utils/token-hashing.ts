import {
  TokenTransaction,
  OperatorSpecificTokenTransactionSignablePayload,
  FreezeTokensPayload,
} from "../proto/spark";
import { sha256 } from "@scure/btc-signer/utils";

export function hashTokenTransaction(
  tokenTransaction: TokenTransaction,
  partialHash: boolean = false
): Uint8Array {
  if (!tokenTransaction) {
    throw new Error("token transaction cannot be nil");
  }

  let allHashes: Uint8Array[] = [];

  // Hash input leaves if a transfer
  if (tokenTransaction.tokenInput?.$case === "transferInput") {
    // Hash leaves_to_spend
    for (const leaf of tokenTransaction.tokenInput!.transferInput!
      .leavesToSpend || []) {
      const hashObj = sha256.create();

      if (leaf.prevTokenTransactionHash) {
        hashObj.update(leaf.prevTokenTransactionHash);
      }

      const voutBytes = new Uint8Array(4);
      new DataView(voutBytes.buffer).setUint32(
        0,
        leaf.prevTokenTransactionLeafVout,
        false
      ); // false for big-endian
      hashObj.update(voutBytes);

      allHashes.push(hashObj.digest());
    }
  }

  // Hash input issuance if an issuance
  if (tokenTransaction.tokenInput?.$case === "mintInput") {
    const hashObj = sha256.create();
    if (tokenTransaction.tokenInput.mintInput!.issuerPublicKey) {
      hashObj.update(tokenTransaction.tokenInput.mintInput!.issuerPublicKey);
    }
    if (tokenTransaction.tokenInput.mintInput!.issuerProvidedTimestamp) {
      const timestampBytes = new Uint8Array(8);
      new DataView(timestampBytes.buffer).setBigUint64(
        0,
        BigInt(tokenTransaction.tokenInput.mintInput!.issuerProvidedTimestamp),
        true // true for little-endian to match Go implementation
      );
      hashObj.update(timestampBytes);
    }
    allHashes.push(hashObj.digest());
  }

  // Hash output leaves
  for (const leaf of tokenTransaction.outputLeaves || []) {
    const hashObj = sha256.create();

    // Only hash ID if it's not empty and not in partial hash mode
    if (leaf.id && !partialHash) {
      hashObj.update(new TextEncoder().encode(leaf.id));
    }
    if (leaf.ownerPublicKey) {
      hashObj.update(leaf.ownerPublicKey);
    }
    if (leaf.revocationPublicKey && !partialHash) {
      hashObj.update(leaf.revocationPublicKey);
    }

    if (leaf.withdrawBondSats && !partialHash) {
      const bondBytes = new Uint8Array(8);
      new DataView(bondBytes.buffer).setBigUint64(
        0,
        BigInt(leaf.withdrawBondSats!),
        false
      );
      hashObj.update(bondBytes);
    }

    if (leaf.withdrawRelativeBlockLocktime && !partialHash) {
      const locktimeBytes = new Uint8Array(8);
      new DataView(locktimeBytes.buffer).setBigUint64(
        0,
        BigInt(leaf.withdrawRelativeBlockLocktime!),
        false
      );
      hashObj.update(locktimeBytes);
    }

    if (leaf.tokenPublicKey) {
      hashObj.update(leaf.tokenPublicKey);
    }
    if (leaf.tokenAmount) {
      hashObj.update(leaf.tokenAmount);
    }

    allHashes.push(hashObj.digest());
  }

  // Sort operator public keys before hashing
  const sortedPubKeys = [...(tokenTransaction.sparkOperatorIdentityPublicKeys || [])].sort(
    (a, b) => {
      for (let i = 0; i < a.length && i < b.length; i++) {
        if (a[i] !== b[i]) return a[i] - b[i];
      }
      return a.length - b.length;
    }
  );

  // Hash spark operator identity public keys
  for (const pubKey of sortedPubKeys) {
    const hashObj = sha256.create();
    if (pubKey) {
      hashObj.update(pubKey);
    }
    allHashes.push(hashObj.digest());
  }

  // Final hash of all concatenated hashes
  const finalHashObj = sha256.create();
  const concatenatedHashes = new Uint8Array(
    allHashes.reduce((sum, hash) => sum + hash.length, 0)
  );
  let offset = 0;
  for (const hash of allHashes) {
    concatenatedHashes.set(hash, offset);
    offset += hash.length;
  }
  finalHashObj.update(concatenatedHashes);
  return finalHashObj.digest();
}

export function hashOperatorSpecificTokenTransactionSignablePayload(
  payload: OperatorSpecificTokenTransactionSignablePayload
): Uint8Array {
  if (!payload) {
    throw new Error("revocation keyshare signable payload cannot be nil");
  }

  let allHashes = new Uint8Array(0);

  // Hash final_token_transaction_hash
  const hash1 = sha256(payload.finalTokenTransactionHash || new Uint8Array(0));
  allHashes = concatenateUint8Arrays(allHashes, hash1);

  // Hash operator_identity_public_key
  const hash2 = sha256(payload.operatorIdentityPublicKey || new Uint8Array(0));
  allHashes = concatenateUint8Arrays(allHashes, hash2);

  // Final hash of all concatenated hashes
  return sha256(allHashes);
}

export function hashFreezeTokensPayload(
  payload: FreezeTokensPayload
): Uint8Array {
  if (!payload) {
    throw new Error("freeze tokens payload cannot be nil");
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
      true // true for little-endian
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

function concatenateUint8Arrays(array1: Uint8Array, array2: Uint8Array) {
  const result = new Uint8Array(array1.length + array2.length);
  result.set(array1, 0);
  result.set(array2, array1.length);
  return result;
}