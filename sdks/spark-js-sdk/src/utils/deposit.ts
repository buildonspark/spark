import { Address } from "proto/spark";
import { subtractPublicKeys } from "./keys";
import { proofOfPossessionMessageHashForDepositAddress } from "./proof";
import { sha256 } from "@scure/btc-signer/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import * as btc from "@scure/btc-signer";

export type SigningOperator = {
  id: number;
  identifier: string;
  address: string;
  identityPublicKey: Uint8Array;
};

export function validateDepositAddress(
  address: Address,
  userPubkey: Uint8Array,
  identityPublicKey: Uint8Array,
  signingOperators: SigningOperator[],
  coordinatorIdentifier: string
) {
  if (
    !address.depositAddressProof ||
    !address.depositAddressProof.proofOfPossessionSignature ||
    !address.depositAddressProof.addressSignatures
  ) {
    throw new Error(
      "proof of possession signature or address signatures is null"
    );
  }

  const operatorPubkey = subtractPublicKeys(address.verifyingKey, userPubkey);
  const msg = proofOfPossessionMessageHashForDepositAddress(
    identityPublicKey,
    operatorPubkey,
    address.address
  );

  const taprootKey = btc.p2tr(
    operatorPubkey.slice(1, 33),
    undefined,
    btc.NETWORK
  ).tweakedPubkey;

  const sig = secp256k1.Signature.fromCompact(
    address.depositAddressProof.proofOfPossessionSignature
  );

  // mneuomic
  // seed
  // remote signer

  const isVerified = schnorr.verify(
    address.depositAddressProof.proofOfPossessionSignature,
    msg,
    taprootKey
  );

  if (!isVerified) {
    throw new Error("proof of possession signature verification failed");
  }

  const addrHash = sha256(address.address);
  for (const operator of signingOperators) {
    if (operator.identifier === coordinatorIdentifier) {
      continue;
    }

    const operatorPubkey = operator.identityPublicKey;
    const operatorSig =
      address.depositAddressProof.addressSignatures[operator.identifier];

    const sig = secp256k1.Signature.fromDER(operatorSig);

    const isVerified = secp256k1.verify(sig, addrHash, operatorPubkey);
    if (!isVerified) {
      throw new Error("signature verification failed");
    }
  }
}
