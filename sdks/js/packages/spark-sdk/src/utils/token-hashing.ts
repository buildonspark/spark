import { sha256 } from "@scure/btc-signer/utils";
import {
  OperatorSpecificTokenTransactionSignablePayload,
  TokenTransaction,
} from "../proto/spark.js";

export function hashTokenTransaction(
  tokenTransaction: TokenTransaction,
  partialHash: boolean = false,
): Uint8Array {
  if (!tokenTransaction) {
    throw new Error("token transaction cannot be nil");
  }

  let allHashes: Uint8Array[] = [];

  // Hash input leaves if a transfer
  if (tokenTransaction.tokenInput?.$case === "transferInput") {
    if (!tokenTransaction.tokenInput.transferInput.leavesToSpend) {
      throw new Error("leaves to spend cannot be null");
    }

    if (tokenTransaction.tokenInput.transferInput.leavesToSpend.length === 0) {
      throw new Error("leaves to spend cannot be empty");
    }

    // Hash leaves_to_spend
    for (const [i, leaf] of (tokenTransaction.tokenInput!.transferInput!
      .leavesToSpend).entries()) {

      if (!leaf) {
        throw new Error(`leaf cannot be null at index ${i}`);
      }

      const hashObj = sha256.create();

      if (leaf.prevTokenTransactionHash) {
        const prevHash = leaf.prevTokenTransactionHash;
        if (leaf.prevTokenTransactionHash.length !== 32) {
          throw new Error(`invalid previous transaction hash length at index ${i}: expected 32 bytes, got ${prevHash}`);
        }
        hashObj.update(leaf.prevTokenTransactionHash);
      }

      const voutBytes = new Uint8Array(4);
      new DataView(voutBytes.buffer).setUint32(
        0,
        leaf.prevTokenTransactionLeafVout,
        false,
      ); // false for big-endian
      hashObj.update(voutBytes);

      allHashes.push(hashObj.digest());
    }
  }

  // Hash input issuance if a mint
  if (tokenTransaction.tokenInput?.$case === "mintInput") {
    const hashObj = sha256.create();

    if (tokenTransaction.tokenInput.mintInput!.issuerPublicKey) {
        const issuerPubKey: Uint8Array = tokenTransaction.tokenInput.mintInput.issuerPublicKey;
        if (issuerPubKey.length === 0) {
          throw new Error("issuer public key cannot be empty");
        }
        hashObj.update(issuerPubKey);

      if (tokenTransaction.tokenInput.mintInput!.issuerProvidedTimestamp != 0) {
        const timestampBytes = new Uint8Array(8);
        new DataView(timestampBytes.buffer).setBigUint64(
          0,
          BigInt(tokenTransaction.tokenInput.mintInput!.issuerProvidedTimestamp),
          true, // true for little-endian to match Go implementation
        );
        hashObj.update(timestampBytes);
      }
      allHashes.push(hashObj.digest());
    }
  }

  // Hash output leaves
  if (!tokenTransaction.outputLeaves) {
    throw new Error("output leaves cannot be null");
  }

  if (tokenTransaction.outputLeaves.length === 0) {
    throw new Error("output leaves cannot be empty");
  }

  for (const [i, leaf] of (tokenTransaction.outputLeaves).entries()) {
    if (!leaf) {
      throw new Error("leaf cannot be null");
    }

    const hashObj = sha256.create();

    // Only hash ID if it's not empty and not in partial hash mode
    if (leaf.id && !partialHash) {
      if (leaf.id.length === 0) {
        throw new Error(`leaf ID at index ${i} cannot be empty`);
      }
      hashObj.update(new TextEncoder().encode(leaf.id));
    }
    if (leaf.ownerPublicKey) {
      if (leaf.ownerPublicKey.length === 0) {
        throw new Error(`owner public key at index ${i} cannot be empty`);
      }
      hashObj.update(leaf.ownerPublicKey);
    }

    if (!partialHash) {
      const revPubKey = leaf.revocationPublicKey!!;
      if (revPubKey) {
        if (revPubKey.length === 0) {
          throw new Error(`revocation public key at index ${i} cannot be empty`);
        }
        hashObj.update(revPubKey);
      }

      const bondBytes = new Uint8Array(8);
      new DataView(bondBytes.buffer).setBigUint64(
        0,
        BigInt(leaf.withdrawBondSats!),
        false,
      );
      hashObj.update(bondBytes);

      const locktimeBytes = new Uint8Array(8);
      new DataView(locktimeBytes.buffer).setBigUint64(
        0,
        BigInt(leaf.withdrawRelativeBlockLocktime!),
        false,
      );
      hashObj.update(locktimeBytes);
    }

    if (leaf.tokenPublicKey) {
      if (leaf.tokenPublicKey.length === 0) {
        throw new Error(`token public key at index ${i} cannot be empty`);
      }
      hashObj.update(leaf.tokenPublicKey);
    }
    if (leaf.tokenAmount) {
      if (leaf.tokenAmount.length === 0) {
        throw new Error(`token amount at index ${i} cannot be empty`);
      }
      if (leaf.tokenAmount.length > 16) {
        throw new Error(`token amount at index ${i} exceeds maximum length; got ${leaf.tokenAmount.length} bytes, max 16`);
      }
      hashObj.update(leaf.tokenAmount);
    }

    allHashes.push(hashObj.digest());
  }

  if (!tokenTransaction.sparkOperatorIdentityPublicKeys) {
    throw new Error("spark operator identity public keys cannot be null");
  }

  // Sort operator public keys before hashing
  const sortedPubKeys = [
    ...(tokenTransaction.sparkOperatorIdentityPublicKeys || []),
  ].sort((a, b) => {
    for (let i = 0; i < a.length && i < b.length; i++) {
      // @ts-ignore - i < a and b length
      if (a[i] !== b[i]) return a[i] - b[i];
    }
    return a.length - b.length;
  });

  // Hash spark operator identity public keys
  for (const [i, pubKey] of sortedPubKeys.entries()) {
    if (!pubKey) {
      throw new Error(`operator public key at index ${i} cannot be null`);
    }
    if (pubKey.length === 0) {
      throw new Error(`operator public key at index ${i} cannot be empty`);
    }
    const hashObj = sha256.create();
    hashObj.update(pubKey);
    allHashes.push(hashObj.digest());
  }

  // Hash the network field
  const hashObj = sha256.create();
  let networkBytes = new Uint8Array(4);
  new DataView(networkBytes.buffer).setUint32(
    0,
    tokenTransaction.network.valueOf(),
    false, // false for big-endian
  );
  hashObj.update(networkBytes);
  allHashes.push(hashObj.digest());

  // Final hash of all concatenated hashes
  const finalHashObj = sha256.create();
  const concatenatedHashes = new Uint8Array(
    allHashes.reduce((sum, hash) => sum + hash.length, 0),
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
  payload: OperatorSpecificTokenTransactionSignablePayload,
): Uint8Array {
  if (!payload) {
    throw new Error("operator specific token transaction signable payload cannot be null");
  }

  let allHashes: Uint8Array[] = [];

  // Hash final token transaction hash if present
  if (payload.finalTokenTransactionHash) {
    const hashObj = sha256.create();
    if (payload.finalTokenTransactionHash.length !== 32) {
      throw new Error(`invalid final token transaction hash length: expected 32 bytes, got ${payload.finalTokenTransactionHash.length}`);
    }
    hashObj.update(payload.finalTokenTransactionHash);
    allHashes.push(hashObj.digest());
  }

  // Hash operator identity public key
  if (!payload.operatorIdentityPublicKey) {
    throw new Error("operator identity public key cannot be null");
  }

  if (payload.operatorIdentityPublicKey.length === 0) {
    throw new Error("operator identity public key cannot be empty");
  }

  const hashObj = sha256.create();
  hashObj.update(payload.operatorIdentityPublicKey);
  allHashes.push(hashObj.digest());

  // Final hash of all concatenated hashes
  const finalHashObj = sha256.create();
  const concatenatedHashes = new Uint8Array(
    allHashes.reduce((sum, hash) => sum + hash.length, 0),
  );
  let offset = 0;
  for (const hash of allHashes) {
    concatenatedHashes.set(hash, offset);
    offset += hash.length;
  }
  finalHashObj.update(concatenatedHashes);
  return finalHashObj.digest();
}
