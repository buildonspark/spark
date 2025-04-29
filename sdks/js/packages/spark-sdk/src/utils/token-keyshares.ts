import { secp256k1 } from "@noble/curves/secp256k1";
import {
  bigIntToPrivateKey,
  recoverSecret,
  VerifiableSecretShare,
} from "./secret-sharing.js";
import { KeyshareWithIndex } from "../proto/spark.js";

export interface KeyshareWithOperatorIndex {
  operatorIndex: number;
  keyshare: KeyshareWithIndex;
}

export function recoverRevocationSecretFromKeyshares(
  keyshares: KeyshareWithOperatorIndex[],
  threshold: number,
): Uint8Array {
  // Convert keyshares to secret shares format
  const shares: VerifiableSecretShare[] = keyshares.map((keyshare) => ({
    fieldModulus: BigInt("0x" + secp256k1.CURVE.n.toString(16)), // secp256k1 curve order
    threshold,
    index: BigInt(keyshare.operatorIndex),
    share: BigInt(
      "0x" + Buffer.from(keyshare.keyshare.keyshare).toString("hex"),
    ),
    proofs: [],
  }));

  const recoveredSecret = recoverSecret(shares);
  return bigIntToPrivateKey(recoveredSecret);
}
