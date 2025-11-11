import { bytesToHex } from "@noble/curves/utils";
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
} from "./wasm/wasm-nodejs.js";
import {
  type AggregateFrostBindingParams,
  type SignFrostBindingParams,
  type IKeyPackage,
  type ISigningCommitment,
  type ISigningNonce,
} from "./types.js";
import { SparkFrostBase } from "./spark-bindings.js";

function createKeyPackage(params: IKeyPackage): KeyPackage {
  return new KeyPackage(
    params.secretKey,
    params.publicKey,
    params.verifyingKey,
  );
}

function createSigningNonce(params: ISigningNonce): SigningNonce {
  return new SigningNonce(params.hiding, params.binding);
}

function createSigningCommitment(
  params: ISigningCommitment,
): SigningCommitment {
  return new SigningCommitment(params.hiding, params.binding);
}

class SparkFrostNodeJS extends SparkFrostBase {
  async signFrost({
    message,
    keyPackage,
    nonce,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostBindingParams) {
    const result = wasm_sign_frost(
      message,
      createKeyPackage(keyPackage),
      createSigningNonce(nonce),
      createSigningCommitment(selfCommitment),
      statechainCommitments,
      adaptorPubKey,
    );
    return Promise.resolve(result);
  }

  aggregateFrost({
    message,
    statechainCommitments,
    selfCommitment,
    statechainSignatures,
    selfSignature,
    statechainPublicKeys,
    selfPublicKey,
    verifyingKey,
    adaptorPubKey,
  }: AggregateFrostBindingParams) {
    const result = wasm_aggregate_frost(
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
    return Promise.resolve(result);
  }

  createDummyTx(address: string, amountSats: bigint) {
    const dummyTx = create_dummy_tx(address, amountSats);
    return Promise.resolve(dummyTx);
  }

  encryptEcies(msg: Uint8Array, publicKey: Uint8Array) {
    const encryptedMsg = encrypt_ecies(msg, publicKey);
    return Promise.resolve(encryptedMsg);
  }

  decryptEcies(encryptedMsg: Uint8Array, privateKey: Uint8Array) {
    const plaintext = decrypt_ecies(encryptedMsg, privateKey);
    return Promise.resolve(plaintext);
  }
}

export { type DummyTx, SparkFrostNodeJS as SparkFrost };
