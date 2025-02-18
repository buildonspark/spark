import { secp256k1 } from "@noble/curves/secp256k1";
import { sha256 } from "@scure/btc-signer/utils";
import {
  OperatorSpecificTokenTransactionSignablePayload,
  TokenTransaction,
} from "../proto/spark";
import {
  bigIntToPrivateKey,
  recoverSecret,
  VerifiableSecretShare,
} from "./secret-sharing";

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
    allHashes.push(hashObj.digest());
  }

  // Hash output leaves
  for (const leaf of tokenTransaction.outputLeaves || []) {
    const hashObj = sha256.create();

    if (leaf.id) {
      hashObj.update(new TextEncoder().encode(leaf.id));
    }
    if (leaf.ownerPublicKey) {
      hashObj.update(leaf.ownerPublicKey);
    }
    if (leaf.revocationPublicKey && !partialHash) {
      hashObj.update(leaf.revocationPublicKey);
    }

    const bondBytes = new Uint8Array(8);
    new DataView(bondBytes.buffer).setBigUint64(
      0,
      BigInt(leaf.withdrawBondSats || 0),
      false
    );
    hashObj.update(bondBytes);

    const locktimeBytes = new Uint8Array(8);
    new DataView(locktimeBytes.buffer).setBigUint64(
      0,
      BigInt(leaf.withdrawRelativeBlockLocktime || 0),
      false
    );
    hashObj.update(locktimeBytes);

    if (leaf.tokenPublicKey) {
      hashObj.update(leaf.tokenPublicKey);
    }
    if (leaf.tokenAmount) {
      hashObj.update(leaf.tokenAmount);
    }

    allHashes.push(hashObj.digest());
  }

  // Hash spark operator identity public keys
  for (const pubKey of tokenTransaction.sparkOperatorIdentityPublicKeys || []) {
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

export interface KeyshareWithOperatorIndex {
  index: number;
  keyshare: Uint8Array;
}

export function recoverPrivateKeyFromKeyshares(
  keyshares: KeyshareWithOperatorIndex[],
  threshold: number
): Uint8Array {
  // Convert keyshares to secret shares format
  const shares: VerifiableSecretShare[] = keyshares.map((keyshare) => ({
    fieldModulus: BigInt("0x" + secp256k1.CURVE.n.toString(16)), // secp256k1 curve order
    threshold,
    index: BigInt(keyshare.index),
    share: BigInt("0x" + Buffer.from(keyshare.keyshare).toString("hex")),
    proofs: [],
  }));

  // Recover the secret
  const recoveredKey = recoverSecret(shares);

  // Convert to bytes
  return bigIntToPrivateKey(recoveredKey);
}

// Helper function to concatenate Uint8Arrays
function concatenateUint8Arrays(array1: Uint8Array, array2: Uint8Array) {
  const result = new Uint8Array(array1.length + array2.length);
  result.set(array1, 0);
  result.set(array2, array1.length);
  return result;
}
