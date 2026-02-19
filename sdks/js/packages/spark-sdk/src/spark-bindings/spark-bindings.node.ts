import { bytesToHex } from "@noble/curves/utils";
import {
  apply_adaptor_to_signature,
  create_dummy_tx,
  decrypt_ecies,
  DummyTx,
  encrypt_ecies,
  generate_adaptor_from_signature,
  generate_signature_from_existing_adaptor,
  KeyPackage,
  SigningCommitment,
  SigningNonce,
  split_secret_with_proofs,
  recover_secret_wasm,
  validate_adaptor_signature,
  validate_share,
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

  splitSecretWithProofs(
    secret: Uint8Array,
    threshold: number,
    numShares: number,
  ) {
    const result = split_secret_with_proofs(secret, threshold, numShares);
    return Promise.resolve(
      (
        result as {
          threshold: number;
          index: number;
          share: number[];
          proofs: number[][];
        }[]
      ).map(
        (s: {
          threshold: number;
          index: number;
          share: number[];
          proofs: number[][];
        }) => ({
          threshold: s.threshold,
          index: s.index,
          share: new Uint8Array(s.share),
          proofs: s.proofs.map((p: number[]) => new Uint8Array(p)),
        }),
      ),
    );
  }

  recoverSecret(
    shares: { threshold: number; index: number; share: Uint8Array }[],
  ) {
    const result = recover_secret_wasm(shares);
    return Promise.resolve(result);
  }

  validateShare(
    share: Uint8Array,
    index: number,
    threshold: number,
    proofs: Uint8Array[],
  ) {
    validate_share(share, index, threshold, proofs);
    return Promise.resolve();
  }

  override generateAdaptorFromSignature(signature: Uint8Array) {
    const result = generate_adaptor_from_signature(signature);
    return {
      adaptorSignature: result.signature,
      adaptorPrivateKey: result.adaptor_private_key,
    };
  }

  override generateSignatureFromExistingAdaptor(
    signature: Uint8Array,
    adaptorPrivateKeyBytes: Uint8Array,
  ) {
    return generate_signature_from_existing_adaptor(
      signature,
      adaptorPrivateKeyBytes,
    );
  }

  override validateAdaptorSignature(
    pubkey: Uint8Array,
    hash: Uint8Array,
    signature: Uint8Array,
    adaptorPubkey: Uint8Array,
  ) {
    validate_adaptor_signature(pubkey, hash, signature, adaptorPubkey);
    return true;
  }

  override applyAdaptorToSignature(
    pubkey: Uint8Array,
    hash: Uint8Array,
    signature: Uint8Array,
    adaptorPrivateKeyBytes: Uint8Array,
  ) {
    return apply_adaptor_to_signature(
      pubkey,
      hash,
      signature,
      adaptorPrivateKeyBytes,
    );
  }
}

export { type DummyTx, SparkFrostNodeJS as SparkFrost };
