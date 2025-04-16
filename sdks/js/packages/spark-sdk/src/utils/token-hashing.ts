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

  // Hash token inputs if a transfer
  if (tokenTransaction.tokenInputs?.$case === "transferInput") {
    if (!tokenTransaction.tokenInputs.transferInput.outputsToSpend) {
      throw new Error("outputs to spend cannot be null");
    }

    if (
      tokenTransaction.tokenInputs.transferInput.outputsToSpend.length === 0
    ) {
      throw new Error("outputs to spend cannot be empty");
    }

    // Hash outputs to spend
    for (const [
      i,
      output,
    ] of tokenTransaction.tokenInputs!.transferInput!.outputsToSpend.entries()) {
      if (!output) {
        throw new Error(`output cannot be null at index ${i}`);
      }

      const hashObj = sha256.create();

      if (output.prevTokenTransactionHash) {
        const prevHash = output.prevTokenTransactionHash;
        if (output.prevTokenTransactionHash.length !== 32) {
          throw new Error(
            `invalid previous transaction hash length at index ${i}: expected 32 bytes, got ${prevHash}`,
          );
        }
        hashObj.update(output.prevTokenTransactionHash);
      }

      const voutBytes = new Uint8Array(4);
      new DataView(voutBytes.buffer).setUint32(
        0,
        output.prevTokenTransactionVout,
        false,
      ); // false for big-endian
      hashObj.update(voutBytes);

      allHashes.push(hashObj.digest());
    }
  }

  // Hash input issuance if a mint
  if (tokenTransaction.tokenInputs?.$case === "mintInput") {
    const hashObj = sha256.create();

    if (tokenTransaction.tokenInputs.mintInput!.issuerPublicKey) {
      const issuerPubKey: Uint8Array =
        tokenTransaction.tokenInputs.mintInput.issuerPublicKey;
      if (issuerPubKey.length === 0) {
        throw new Error("issuer public key cannot be empty");
      }
      hashObj.update(issuerPubKey);

      if (
        tokenTransaction.tokenInputs.mintInput!.issuerProvidedTimestamp != 0
      ) {
        const timestampBytes = new Uint8Array(8);
        new DataView(timestampBytes.buffer).setBigUint64(
          0,
          BigInt(
            tokenTransaction.tokenInputs.mintInput!.issuerProvidedTimestamp,
          ),
          true, // true for little-endian to match Go implementation
        );
        hashObj.update(timestampBytes);
      }
      allHashes.push(hashObj.digest());
    }
  }

  // Hash token outputs
  if (!tokenTransaction.tokenOutputs) {
    throw new Error("token outputs cannot be null");
  }

  if (tokenTransaction.tokenOutputs.length === 0) {
    throw new Error("token outputs cannot be empty");
  }

  for (const [i, output] of tokenTransaction.tokenOutputs.entries()) {
    if (!output) {
      throw new Error("output cannot be null");
    }

    const hashObj = sha256.create();

    // Only hash ID if it's not empty and not in partial hash mode
    if (output.id && !partialHash) {
      if (output.id.length === 0) {
        throw new Error(`output ID at index ${i} cannot be empty`);
      }
      hashObj.update(new TextEncoder().encode(output.id));
    }
    if (output.ownerPublicKey) {
      if (output.ownerPublicKey.length === 0) {
        throw new Error(`owner public key at index ${i} cannot be empty`);
      }
      hashObj.update(output.ownerPublicKey);
    }

    if (!partialHash) {
      const revPubKey = output.revocationCommitment!!;
      if (revPubKey) {
        if (revPubKey.length === 0) {
          throw new Error(`revocation commitmentat index ${i} cannot be empty`);
        }
        hashObj.update(revPubKey);
      }

      const bondBytes = new Uint8Array(8);
      new DataView(bondBytes.buffer).setBigUint64(
        0,
        BigInt(output.withdrawBondSats!),
        false,
      );
      hashObj.update(bondBytes);

      const locktimeBytes = new Uint8Array(8);
      new DataView(locktimeBytes.buffer).setBigUint64(
        0,
        BigInt(output.withdrawRelativeBlockLocktime!),
        false,
      );
      hashObj.update(locktimeBytes);
    }

    if (output.tokenPublicKey) {
      if (output.tokenPublicKey.length === 0) {
        throw new Error(`token public key at index ${i} cannot be empty`);
      }
      hashObj.update(output.tokenPublicKey);
    }
    if (output.tokenAmount) {
      if (output.tokenAmount.length === 0) {
        throw new Error(`token amount at index ${i} cannot be empty`);
      }
      if (output.tokenAmount.length > 16) {
        throw new Error(
          `token amount at index ${i} exceeds maximum length; got ${output.tokenAmount.length} bytes, max 16`,
        );
      }
      hashObj.update(output.tokenAmount);
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
    throw new Error(
      "operator specific token transaction signable payload cannot be null",
    );
  }

  let allHashes: Uint8Array[] = [];

  // Hash final token transaction hash if present
  if (payload.finalTokenTransactionHash) {
    const hashObj = sha256.create();
    if (payload.finalTokenTransactionHash.length !== 32) {
      throw new Error(
        `invalid final token transaction hash length: expected 32 bytes, got ${payload.finalTokenTransactionHash.length}`,
      );
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
