import { sha256 } from "@noble/hashes/sha2";
import { ValidationError } from "../errors/types.js";
import { bech32m } from "@scure/base";
import { SparkAddress } from "../proto/spark.js";
import { OperatorSpecificTokenTransactionSignablePayload } from "../proto/spark.js";
import {
  TokenTransaction,
  TokenTransactionType,
} from "../proto/spark_token.js";

export function hashTokenTransaction(
  tokenTransaction: TokenTransaction,
  partialHash: boolean = false,
): Uint8Array {
  switch (tokenTransaction.version) {
    case 1:
      return hashTokenTransactionV1(tokenTransaction, partialHash);
    case 2:
      return hashTokenTransactionV2(tokenTransaction, partialHash);
    default:
      throw new ValidationError("invalid token transaction version", {
        field: "tokenTransaction.version",
        value: tokenTransaction.version,
      });
  }
}

export function hashTokenTransactionV1(
  tokenTransaction: TokenTransaction,
  partialHash: boolean = false,
): Uint8Array {
  if (!tokenTransaction) {
    throw new ValidationError("token transaction cannot be nil", {
      field: "tokenTransaction",
    });
  }

  let allHashes: Uint8Array[] = [];

  // Hash version
  const versionHashObj = sha256.create();
  const versionBytes = new Uint8Array(4);
  new DataView(versionBytes.buffer).setUint32(
    0,
    tokenTransaction.version,
    false, // false for big-endian
  );
  versionHashObj.update(versionBytes);
  allHashes.push(versionHashObj.digest());

  // Hash transaction type
  const typeHashObj = sha256.create();
  const typeBytes = new Uint8Array(4);
  let transactionType = 0;

  if (tokenTransaction.tokenInputs?.$case === "mintInput") {
    transactionType = TokenTransactionType.TOKEN_TRANSACTION_TYPE_MINT;
  } else if (tokenTransaction.tokenInputs?.$case === "transferInput") {
    transactionType = TokenTransactionType.TOKEN_TRANSACTION_TYPE_TRANSFER;
  } else if (tokenTransaction.tokenInputs?.$case === "createInput") {
    transactionType = TokenTransactionType.TOKEN_TRANSACTION_TYPE_CREATE;
  } else {
    throw new ValidationError(
      "token transaction must have exactly one input type",
      {
        field: "tokenInputs",
      },
    );
  }

  new DataView(typeBytes.buffer).setUint32(0, transactionType, false);
  typeHashObj.update(typeBytes);
  allHashes.push(typeHashObj.digest());

  // Hash token inputs based on type
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

    // Hash outputs to spend length
    const outputsLenHashObj = sha256.create();
    const outputsLenBytes = new Uint8Array(4);
    new DataView(outputsLenBytes.buffer).setUint32(
      0,
      tokenTransaction.tokenInputs.transferInput.outputsToSpend.length,
      false,
    );
    outputsLenHashObj.update(outputsLenBytes);
    allHashes.push(outputsLenHashObj.digest());

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
  } else if (tokenTransaction.tokenInputs?.$case === "mintInput") {
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
      allHashes.push(hashObj.digest());

      const tokenIdentifierHashObj = sha256.create();
      if (tokenTransaction.tokenInputs.mintInput.tokenIdentifier) {
        tokenIdentifierHashObj.update(
          tokenTransaction.tokenInputs.mintInput.tokenIdentifier,
        );
      } else {
        tokenIdentifierHashObj.update(new Uint8Array(32));
      }
      allHashes.push(tokenIdentifierHashObj.digest());
    }
  } else if (tokenTransaction.tokenInputs?.$case === "createInput") {
    const createInput = tokenTransaction.tokenInputs.createInput!;

    // Hash issuer public key
    const issuerPubKeyHashObj = sha256.create();
    if (
      !createInput.issuerPublicKey ||
      createInput.issuerPublicKey.length === 0
    ) {
      throw new ValidationError("issuer public key cannot be nil or empty", {
        field: "tokenInputs.createInput.issuerPublicKey",
      });
    }
    issuerPubKeyHashObj.update(createInput.issuerPublicKey);
    allHashes.push(issuerPubKeyHashObj.digest());

    // Hash token name
    const tokenNameHashObj = sha256.create();
    if (!createInput.tokenName || createInput.tokenName.length === 0) {
      throw new ValidationError("token name cannot be empty", {
        field: "tokenInputs.createInput.tokenName",
      });
    }
    if (createInput.tokenName.length > 20) {
      throw new ValidationError("token name cannot be longer than 20 bytes", {
        field: "tokenInputs.createInput.tokenName",
        value: createInput.tokenName,
        expectedLength: 20,
        actualLength: createInput.tokenName.length,
      });
    }
    const tokenNameEncoder = new TextEncoder();
    tokenNameHashObj.update(tokenNameEncoder.encode(createInput.tokenName));
    allHashes.push(tokenNameHashObj.digest());

    // Hash token ticker
    const tokenTickerHashObj = sha256.create();
    if (!createInput.tokenTicker || createInput.tokenTicker.length === 0) {
      throw new ValidationError("token ticker cannot be empty", {
        field: "tokenInputs.createInput.tokenTicker",
      });
    }
    if (createInput.tokenTicker.length > 6) {
      throw new ValidationError("token ticker cannot be longer than 6 bytes", {
        field: "tokenInputs.createInput.tokenTicker",
        value: createInput.tokenTicker,
        expectedLength: 6,
        actualLength: createInput.tokenTicker.length,
      });
    }
    const tokenTickerEncoder = new TextEncoder();
    tokenTickerHashObj.update(
      tokenTickerEncoder.encode(createInput.tokenTicker),
    );
    allHashes.push(tokenTickerHashObj.digest());

    // Hash decimals
    const decimalsHashObj = sha256.create();
    const decimalsBytes = new Uint8Array(4);
    new DataView(decimalsBytes.buffer).setUint32(
      0,
      createInput.decimals,
      false,
    );
    decimalsHashObj.update(decimalsBytes);
    allHashes.push(decimalsHashObj.digest());

    // Hash max supply (fixed 16 bytes)
    const maxSupplyHashObj = sha256.create();
    if (!createInput.maxSupply) {
      throw new ValidationError("max supply cannot be nil", {
        field: "tokenInputs.createInput.maxSupply",
      });
    }
    if (createInput.maxSupply.length !== 16) {
      throw new ValidationError("max supply must be exactly 16 bytes", {
        field: "tokenInputs.createInput.maxSupply",
        value: createInput.maxSupply,
        expectedLength: 16,
        actualLength: createInput.maxSupply.length,
      });
    }
    maxSupplyHashObj.update(createInput.maxSupply);
    allHashes.push(maxSupplyHashObj.digest());

    // Hash is freezable
    const isFreezableHashObj = sha256.create();
    isFreezableHashObj.update(
      new Uint8Array([createInput.isFreezable ? 1 : 0]),
    );
    allHashes.push(isFreezableHashObj.digest());

    // Hash creation entity public key (only for final hash)
    const creationEntityHashObj = sha256.create();
    if (!partialHash && createInput.creationEntityPublicKey) {
      creationEntityHashObj.update(createInput.creationEntityPublicKey);
    }
    allHashes.push(creationEntityHashObj.digest());
  }

  // Hash token outputs (length + contents)
  if (!tokenTransaction.tokenOutputs) {
    throw new ValidationError("token outputs cannot be null", {
      field: "tokenOutputs",
    });
  }

  // Hash outputs length
  const outputsLenHashObj = sha256.create();
  const outputsLenBytes = new Uint8Array(4);
  new DataView(outputsLenBytes.buffer).setUint32(
    0,
    tokenTransaction.tokenOutputs.length,
    false,
  );
  outputsLenHashObj.update(outputsLenBytes);
  allHashes.push(outputsLenHashObj.digest());

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

    // Hash token public key (33 bytes if present, otherwise 33 zero bytes)
    if (!output.tokenPublicKey || output.tokenPublicKey.length === 0) {
      hashObj.update(new Uint8Array(33));
    } else {
      hashObj.update(output.tokenPublicKey);
    }

    // Hash token identifier (32 bytes if present, otherwise 32 zero bytes)
    if (!output.tokenIdentifier || output.tokenIdentifier.length === 0) {
      hashObj.update(new Uint8Array(32));
    } else {
      hashObj.update(output.tokenIdentifier);
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

  // Hash spark operator identity public keys length
  const operatorLenHashObj = sha256.create();
  const operatorLenBytes = new Uint8Array(4);
  new DataView(operatorLenBytes.buffer).setUint32(
    0,
    sortedPubKeys.length,
    false,
  );
  operatorLenHashObj.update(operatorLenBytes);
  allHashes.push(operatorLenHashObj.digest());

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

  // Hash client created timestamp
  const clientTimestampHashObj = sha256.create();
  const clientCreatedTs: Date | undefined = (tokenTransaction as any)
    .clientCreatedTimestamp;
  if (!clientCreatedTs) {
    throw new ValidationError(
      "client created timestamp cannot be null for V1 token transactions",
      {
        field: "clientCreatedTimestamp",
      },
    );
  }
  const clientUnixTime = clientCreatedTs.getTime();
  const clientTimestampBytes = new Uint8Array(8);
  new DataView(clientTimestampBytes.buffer).setBigUint64(
    0,
    BigInt(clientUnixTime),
    false,
  );
  clientTimestampHashObj.update(clientTimestampBytes);
  allHashes.push(clientTimestampHashObj.digest());

  if (!partialHash) {
    // Hash expiry time
    const expiryHashObj = sha256.create();
    const expiryTimeBytes = new Uint8Array(8);
    const expiryUnixTime = tokenTransaction.expiryTime
      ? Math.floor(tokenTransaction.expiryTime.getTime() / 1000)
      : 0;
    new DataView(expiryTimeBytes.buffer).setBigUint64(
      0,
      BigInt(expiryUnixTime),
      false, // false for big-endian
    );
    expiryHashObj.update(expiryTimeBytes);
    allHashes.push(expiryHashObj.digest());
  }

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

export function hashTokenTransactionV2(
  tokenTransaction: TokenTransaction,
  partialHash: boolean = false,
): Uint8Array {
  if (!tokenTransaction) {
    throw new ValidationError("token transaction cannot be nil", {
      field: "tokenTransaction",
    });
  }

  let allHashes: Uint8Array[] = [];

  // Hash version
  const versionHashObj = sha256.create();
  const versionBytes = new Uint8Array(4);
  new DataView(versionBytes.buffer).setUint32(
    0,
    tokenTransaction.version,
    false, // false for big-endian
  );
  versionHashObj.update(versionBytes);
  allHashes.push(versionHashObj.digest());

  // Hash transaction type
  const typeHashObj = sha256.create();
  const typeBytes = new Uint8Array(4);
  let transactionType = 0;

  if (tokenTransaction.tokenInputs?.$case === "mintInput") {
    transactionType = TokenTransactionType.TOKEN_TRANSACTION_TYPE_MINT;
  } else if (tokenTransaction.tokenInputs?.$case === "transferInput") {
    transactionType = TokenTransactionType.TOKEN_TRANSACTION_TYPE_TRANSFER;
  } else if (tokenTransaction.tokenInputs?.$case === "createInput") {
    transactionType = TokenTransactionType.TOKEN_TRANSACTION_TYPE_CREATE;
  } else {
    throw new ValidationError(
      "token transaction must have exactly one input type",
      {
        field: "tokenInputs",
      },
    );
  }

  new DataView(typeBytes.buffer).setUint32(0, transactionType, false);
  typeHashObj.update(typeBytes);
  allHashes.push(typeHashObj.digest());

  // Hash token inputs based on type
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

    // Hash outputs to spend length
    const outputsLenHashObj = sha256.create();
    const outputsLenBytes = new Uint8Array(4);
    new DataView(outputsLenBytes.buffer).setUint32(
      0,
      tokenTransaction.tokenInputs.transferInput.outputsToSpend.length,
      false,
    );
    outputsLenHashObj.update(outputsLenBytes);
    allHashes.push(outputsLenHashObj.digest());

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
  } else if (tokenTransaction.tokenInputs?.$case === "mintInput") {
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
      allHashes.push(hashObj.digest());

      const tokenIdentifierHashObj = sha256.create();
      if (tokenTransaction.tokenInputs.mintInput.tokenIdentifier) {
        tokenIdentifierHashObj.update(
          tokenTransaction.tokenInputs.mintInput.tokenIdentifier,
        );
      } else {
        tokenIdentifierHashObj.update(new Uint8Array(32));
      }
      allHashes.push(tokenIdentifierHashObj.digest());
    }
  } else if (tokenTransaction.tokenInputs?.$case === "createInput") {
    const createInput = tokenTransaction.tokenInputs.createInput!;

    // Hash issuer public key
    const issuerPubKeyHashObj = sha256.create();
    if (
      !createInput.issuerPublicKey ||
      createInput.issuerPublicKey.length === 0
    ) {
      throw new ValidationError("issuer public key cannot be nil or empty", {
        field: "tokenInputs.createInput.issuerPublicKey",
      });
    }
    issuerPubKeyHashObj.update(createInput.issuerPublicKey);
    allHashes.push(issuerPubKeyHashObj.digest());

    // Hash token name
    const tokenNameHashObj = sha256.create();
    if (!createInput.tokenName || createInput.tokenName.length === 0) {
      throw new ValidationError("token name cannot be empty", {
        field: "tokenInputs.createInput.tokenName",
      });
    }
    if (createInput.tokenName.length > 20) {
      throw new ValidationError("token name cannot be longer than 20 bytes", {
        field: "tokenInputs.createInput.tokenName",
        value: createInput.tokenName,
        expectedLength: 20,
        actualLength: createInput.tokenName.length,
      });
    }
    const tokenNameEncoder = new TextEncoder();
    tokenNameHashObj.update(tokenNameEncoder.encode(createInput.tokenName));
    allHashes.push(tokenNameHashObj.digest());

    // Hash token ticker
    const tokenTickerHashObj = sha256.create();
    if (!createInput.tokenTicker || createInput.tokenTicker.length === 0) {
      throw new ValidationError("token ticker cannot be empty", {
        field: "tokenInputs.createInput.tokenTicker",
      });
    }
    if (createInput.tokenTicker.length > 6) {
      throw new ValidationError("token ticker cannot be longer than 6 bytes", {
        field: "tokenInputs.createInput.tokenTicker",
        value: createInput.tokenTicker,
        expectedLength: 6,
        actualLength: createInput.tokenTicker.length,
      });
    }
    const tokenTickerEncoder = new TextEncoder();
    tokenTickerHashObj.update(
      tokenTickerEncoder.encode(createInput.tokenTicker),
    );
    allHashes.push(tokenTickerHashObj.digest());

    // Hash decimals
    const decimalsHashObj = sha256.create();
    const decimalsBytes = new Uint8Array(4);
    new DataView(decimalsBytes.buffer).setUint32(
      0,
      createInput.decimals,
      false,
    );
    decimalsHashObj.update(decimalsBytes);
    allHashes.push(decimalsHashObj.digest());

    // Hash max supply (fixed 16 bytes)
    const maxSupplyHashObj = sha256.create();
    if (!createInput.maxSupply) {
      throw new ValidationError("max supply cannot be nil", {
        field: "tokenInputs.createInput.maxSupply",
      });
    }
    if (createInput.maxSupply.length !== 16) {
      throw new ValidationError("max supply must be exactly 16 bytes", {
        field: "tokenInputs.createInput.maxSupply",
        value: createInput.maxSupply,
        expectedLength: 16,
        actualLength: createInput.maxSupply.length,
      });
    }
    maxSupplyHashObj.update(createInput.maxSupply);
    allHashes.push(maxSupplyHashObj.digest());

    // Hash is freezable
    const isFreezableHashObj = sha256.create();
    isFreezableHashObj.update(
      new Uint8Array([createInput.isFreezable ? 1 : 0]),
    );
    allHashes.push(isFreezableHashObj.digest());

    // Hash creation entity public key (only for final hash)
    const creationEntityHashObj = sha256.create();
    if (!partialHash && createInput.creationEntityPublicKey) {
      creationEntityHashObj.update(createInput.creationEntityPublicKey);
    }
    allHashes.push(creationEntityHashObj.digest());
  }

  // Hash token outputs (length + contents)
  if (!tokenTransaction.tokenOutputs) {
    throw new ValidationError("token outputs cannot be null", {
      field: "tokenOutputs",
    });
  }

  // Hash outputs length
  const outputsLenHashObj = sha256.create();
  const outputsLenBytes = new Uint8Array(4);
  new DataView(outputsLenBytes.buffer).setUint32(
    0,
    tokenTransaction.tokenOutputs.length,
    false,
  );
  outputsLenHashObj.update(outputsLenBytes);
  allHashes.push(outputsLenHashObj.digest());

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

    // Hash token public key (33 bytes if present, otherwise 33 zero bytes)
    if (!output.tokenPublicKey || output.tokenPublicKey.length === 0) {
      hashObj.update(new Uint8Array(33));
    } else {
      hashObj.update(output.tokenPublicKey);
    }

    // Hash token identifier (32 bytes if present, otherwise 32 zero bytes)
    if (!output.tokenIdentifier || output.tokenIdentifier.length === 0) {
      hashObj.update(new Uint8Array(32));
    } else {
      hashObj.update(output.tokenIdentifier);
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

  // Hash spark operator identity public keys length
  const operatorLenHashObj = sha256.create();
  const operatorLenBytes = new Uint8Array(4);
  new DataView(operatorLenBytes.buffer).setUint32(
    0,
    sortedPubKeys.length,
    false,
  );
  operatorLenHashObj.update(operatorLenBytes);
  allHashes.push(operatorLenHashObj.digest());

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

  // Hash client created timestamp
  const clientTimestampHashObj = sha256.create();
  const clientCreatedTs: Date | undefined = (tokenTransaction as any)
    .clientCreatedTimestamp;
  if (!clientCreatedTs) {
    throw new ValidationError(
      "client created timestamp cannot be null for V1 token transactions",
      {
        field: "clientCreatedTimestamp",
      },
    );
  }
  const clientUnixTime = clientCreatedTs.getTime();
  const clientTimestampBytes = new Uint8Array(8);
  new DataView(clientTimestampBytes.buffer).setBigUint64(
    0,
    BigInt(clientUnixTime),
    false,
  );
  clientTimestampHashObj.update(clientTimestampBytes);
  allHashes.push(clientTimestampHashObj.digest());

  if (!partialHash) {
    // Hash expiry time
    const expiryHashObj = sha256.create();
    const expiryTimeBytes = new Uint8Array(8);
    const expiryUnixTime = tokenTransaction.expiryTime
      ? Math.floor(tokenTransaction.expiryTime.getTime() / 1000)
      : 0;
    new DataView(expiryTimeBytes.buffer).setBigUint64(
      0,
      BigInt(expiryUnixTime),
      false, // false for big-endian
    );
    expiryHashObj.update(expiryTimeBytes);
    allHashes.push(expiryHashObj.digest());
  }

  // Hash invoice attachments
  const attachments = tokenTransaction.invoiceAttachments;

  // Hash attachments length (uint32 BE)
  const lenHash = sha256.create();
  const lenBytes = new Uint8Array(4);
  new DataView(lenBytes.buffer).setUint32(
    0,
    attachments ? attachments.length : 0,
    false,
  );
  lenHash.update(lenBytes);
  allHashes.push(lenHash.digest());

  type Keyed = { id: Uint8Array; raw: string };
  const sortedInvoices: Keyed[] = [];

  if (attachments) {
    for (let i = 0; i < attachments.length; i++) {
      const attachment = attachments[i];
      if (!attachment) {
        throw new ValidationError(
          `invoice attachment at index ${i} cannot be null`,
          {
            field: `invoiceAttachments[${i}]`,
            index: i,
          },
        );
      }
      const invoice = attachment.sparkInvoice;

      let idBytes: Uint8Array | undefined;
      try {
        const decoded = bech32m.decode(invoice as any, 500);
        const payload = SparkAddress.decode(bech32m.fromWords(decoded.words));
        if (!payload.sparkInvoiceFields || !payload.sparkInvoiceFields.id) {
          throw new Error("missing spark invoice fields or id");
        }
        idBytes = payload.sparkInvoiceFields.id;
      } catch (err) {
        throw new ValidationError(
          `invalid invoice at ${i}`,
          {
            field: `invoiceAttachments[${i}].sparkInvoice`,
            index: i,
            value: invoice,
          },
          err as Error,
        );
      }
      if (!idBytes || idBytes.length !== 16) {
        throw new ValidationError(`invalid invoice id at ${i}`, {
          field: `invoiceAttachments[${i}].sparkInvoice`,
          index: i,
        });
      }
      sortedInvoices.push({ id: idBytes, raw: invoice });
    }
  }

  // Sort by UUID bytes (lexicographically)
  sortedInvoices.sort((a, b) => {
    for (let j = 0; j < a.id.length && j < b.id.length; j++) {
      const av = a.id[j] as number;
      const bv = b.id[j] as number;
      if (av !== bv) return av - bv;
    }
    return a.id.length - b.id.length;
  });

  // Hash raw invoice strings (UTF-8)
  const encoder = new TextEncoder();
  for (const k of sortedInvoices) {
    const h = sha256.create();
    h.update(encoder.encode(k.raw));
    allHashes.push(h.digest());
  }

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
