import { sha256 } from "@scure/btc-signer/utils";
import {
  OperatorSpecificTokenTransactionSignablePayload,
  TokenTransaction,
} from "../proto/spark.js";
import { ValidationError } from "../errors/types.js";

export function hashTokenTransaction(
  tokenTransaction: TokenTransaction,
  partialHash: boolean = false,
): Uint8Array {
  if (!tokenTransaction) {
    throw new ValidationError("token transaction cannot be nil", {
      field: "tokenTransaction",
    });
  }

  let allHashes: Uint8Array[] = [];

  // Hash token inputs if a transfer
  if (tokenTransaction.tokenInputs?.$case === "transferInput") {
    if (!tokenTransaction.tokenInputs.transferInput.outputsToSpend) {
      throw new ValidationError("outputs to spend cannot be null", {
        field: "tokenInputs.transferInput.outputsToSpend",
      });
    }

    if (
      tokenTransaction.tokenInputs.transferInput.outputsToSpend.length === 0
    ) {
      throw new ValidationError("outputs to spend cannot be empty", {
        field: "tokenInputs.transferInput.outputsToSpend",
      });
    }

    // Hash outputs to spend
    for (const [
      i,
      output,
    ] of tokenTransaction.tokenInputs!.transferInput!.outputsToSpend.entries()) {
      if (!output) {
        throw new ValidationError(`output cannot be null at index ${i}`, {
          field: `tokenInputs.transferInput.outputsToSpend[${i}]`,
          index: i,
        });
      }

      const hashObj = sha256.create();

      if (output.prevTokenTransactionHash) {
        const prevHash = output.prevTokenTransactionHash;
        if (output.prevTokenTransactionHash.length !== 32) {
          throw new ValidationError(
            `invalid previous transaction hash length at index ${i}`,
            {
              field: `tokenInputs.transferInput.outputsToSpend[${i}].prevTokenTransactionHash`,
              value: prevHash,
              expectedLength: 32,
              actualLength: prevHash.length,
              index: i,
            },
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
        throw new ValidationError("issuer public key cannot be empty", {
          field: "tokenInputs.mintInput.issuerPublicKey",
          value: issuerPubKey,
          expectedLength: 1,
          actualLength: 0,
        });
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
    throw new ValidationError("token outputs cannot be null", {
      field: "tokenOutputs",
    });
  }

  if (tokenTransaction.tokenOutputs.length === 0) {
    throw new ValidationError("token outputs cannot be empty", {
      field: "tokenOutputs",
    });
  }

  for (const [i, output] of tokenTransaction.tokenOutputs.entries()) {
    if (!output) {
      throw new ValidationError(`output cannot be null at index ${i}`, {
        field: `tokenOutputs[${i}]`,
        index: i,
      });
    }

    const hashObj = sha256.create();

    // Only hash ID if it's not empty and not in partial hash mode
    if (output.id && !partialHash) {
      if (output.id.length === 0) {
        throw new ValidationError(`output ID at index ${i} cannot be empty`, {
          field: `tokenOutputs[${i}].id`,
          index: i,
        });
      }
      hashObj.update(new TextEncoder().encode(output.id));
    }
    if (output.ownerPublicKey) {
      if (output.ownerPublicKey.length === 0) {
        throw new ValidationError(
          `owner public key at index ${i} cannot be empty`,
          {
            field: `tokenOutputs[${i}].ownerPublicKey`,
            index: i,
          },
        );
      }
      hashObj.update(output.ownerPublicKey);
    }

    if (!partialHash) {
      const revPubKey = output.revocationCommitment!!;
      if (revPubKey) {
        if (revPubKey.length === 0) {
          throw new ValidationError(
            `revocation commitment at index ${i} cannot be empty`,
            {
              field: `tokenOutputs[${i}].revocationCommitment`,
              index: i,
            },
          );
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
        throw new ValidationError(
          `token public key at index ${i} cannot be empty`,
          {
            field: `tokenOutputs[${i}].tokenPublicKey`,
            index: i,
          },
        );
      }
      hashObj.update(output.tokenPublicKey);
    }
    if (output.tokenAmount) {
      if (output.tokenAmount.length === 0) {
        throw new ValidationError(
          `token amount at index ${i} cannot be empty`,
          {
            field: `tokenOutputs[${i}].tokenAmount`,
            index: i,
          },
        );
      }
      if (output.tokenAmount.length > 16) {
        throw new ValidationError(
          `token amount at index ${i} exceeds maximum length`,
          {
            field: `tokenOutputs[${i}].tokenAmount`,
            value: output.tokenAmount,
            expectedLength: 16,
            actualLength: output.tokenAmount.length,
            index: i,
          },
        );
      }
      hashObj.update(output.tokenAmount);
    }

    allHashes.push(hashObj.digest());
  }

  if (!tokenTransaction.sparkOperatorIdentityPublicKeys) {
    throw new ValidationError(
      "spark operator identity public keys cannot be null",
      {},
    );
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
      throw new ValidationError(
        `operator public key at index ${i} cannot be null`,
        {
          field: `sparkOperatorIdentityPublicKeys[${i}]`,
          index: i,
        },
      );
    }
    if (pubKey.length === 0) {
      throw new ValidationError(
        `operator public key at index ${i} cannot be empty`,
        {
          field: `sparkOperatorIdentityPublicKeys[${i}]`,
          index: i,
        },
      );
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
    throw new ValidationError(
      "operator specific token transaction signable payload cannot be null",
      {
        field: "payload",
      },
    );
  }

  let allHashes: Uint8Array[] = [];

  // Hash final token transaction hash if present
  if (payload.finalTokenTransactionHash) {
    const hashObj = sha256.create();
    if (payload.finalTokenTransactionHash.length !== 32) {
      throw new ValidationError(`invalid final token transaction hash length`, {
        field: "finalTokenTransactionHash",
        value: payload.finalTokenTransactionHash,
        expectedLength: 32,
        actualLength: payload.finalTokenTransactionHash.length,
      });
    }
    hashObj.update(payload.finalTokenTransactionHash);
    allHashes.push(hashObj.digest());
  }

  // Hash operator identity public key
  if (!payload.operatorIdentityPublicKey) {
    throw new ValidationError("operator identity public key cannot be null", {
      field: "operatorIdentityPublicKey",
    });
  }

  if (payload.operatorIdentityPublicKey.length === 0) {
    throw new ValidationError("operator identity public key cannot be empty", {
      field: "operatorIdentityPublicKey",
    });
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
