import { secp256k1 } from "@noble/curves/secp256k1";
import {
  recoverSecret,
  bigIntToPrivateKey,
  VerifiableSecretShare,
} from "./secret-sharing";

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
