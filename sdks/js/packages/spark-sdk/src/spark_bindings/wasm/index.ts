import {
  create_dummy_tx,
  decrypt_ecies,
  DummyTx,
  encrypt_ecies,
  KeyPackage,
  SigningCommitment,
  SigningNonce,
  wasm_aggregate_frost,
  wasm_sign_frost,
} from "../../wasm/spark_bindings.js";
import {
  AggregateFrostParams,
  IKeyPackage,
  ISigningCommitment,
  ISigningNonce,
  SignFrostParams,
} from "../types.js";

export function createKeyPackage(params: IKeyPackage): KeyPackage {
  return new KeyPackage(
    params.secretKey,
    params.publicKey,
    params.verifyingKey,
  );
}

export function createSigningNonce(params: ISigningNonce): SigningNonce {
  return new SigningNonce(params.hiding, params.binding);
}

export function createSigningCommitment(
  params: ISigningCommitment,
): SigningCommitment {
  return new SigningCommitment(params.hiding, params.binding);
}

export function signFrost({
  message,
  keyPackage,
  nonce,
  selfCommitment,
  statechainCommitments,
  adaptorPubKey,
}: SignFrostParams): Uint8Array {
  return wasm_sign_frost(
    message,
    createKeyPackage(keyPackage),
    createSigningNonce(nonce),
    createSigningCommitment(selfCommitment),
    statechainCommitments,
    adaptorPubKey,
  );
}

export function aggregateFrost({
  message,
  statechainCommitments,
  selfCommitment,
  statechainSignatures,
  selfSignature,
  statechainPublicKeys,
  selfPublicKey,
  verifyingKey,
  adaptorPubKey,
}: AggregateFrostParams): Uint8Array {
  return wasm_aggregate_frost(
    message,
    statechainCommitments,
    createSigningCommitment(selfCommitment),
    statechainSignatures,
    selfSignature,
    statechainPublicKeys,
    selfPublicKey,
    verifyingKey,
    adaptorPubKey,
  );
}

export function createDummyTx({
  address,
  amountSats,
}: {
  address: string;
  amountSats: bigint;
}): DummyTx {
  return create_dummy_tx(address, amountSats);
}

export function encryptEcies({
  msg,
  publicKey,
}: {
  msg: Uint8Array;
  publicKey: Uint8Array;
}): Uint8Array {
  return encrypt_ecies(msg, publicKey);
}

export function decryptEcies({
  encryptedMsg,
  privateKey,
}: {
  encryptedMsg: Uint8Array;
  privateKey: Uint8Array;
}): Uint8Array {
  return decrypt_ecies(encryptedMsg, privateKey);
}
