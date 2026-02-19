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
  default as initWasm,
  InitOutput,
} from "./wasm/wasm-browser.js";
import {
  AggregateFrostBindingParams,
  SignFrostBindingParams,
  type IKeyPackage,
  type ISigningCommitment,
  type ISigningNonce,
} from "./types.js";
import { SparkFrostBase } from "./spark-bindings.js";
import wasmBytes from "./wasm/wasm-browser-bg.wasm";

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

class SparkFrostBrowser extends SparkFrostBase {
  /* initPromise needs to be static/global to prevent multiple WASM initializations
     which can intermittently cause memory access errors: */
  private static initPromise: Promise<InitOutput> | null = null;
  private static initialized = false;
  private static initError: Error | null = null;

  private async init(): Promise<void> {
    if (SparkFrostBrowser.initialized) {
      return;
    }

    if (SparkFrostBrowser.initError) {
      throw new Error(
        `SparkFrost: WASM module failed to initialize: ${SparkFrostBrowser.initError.message}`,
      );
    }

    if (SparkFrostBrowser.initPromise) {
      await SparkFrostBrowser.initPromise;
      return;
    }

    SparkFrostBrowser.initPromise = (async () => {
      try {
        const result = await initWasm({ module_or_path: wasmBytes });
        SparkFrostBrowser.initialized = true;
        return result;
      } catch (err) {
        console.error("SparkFrost: WASM initialization failed:", err);
        SparkFrostBrowser.initPromise = null;
        SparkFrostBrowser.initError =
          err instanceof Error ? err : new Error(String(err));
        throw SparkFrostBrowser.initError;
      }
    })();

    await SparkFrostBrowser.initPromise;
  }

  async signFrost({
    message,
    keyPackage,
    nonce,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostBindingParams) {
    await this.init();
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

  async aggregateFrost({
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
    await this.init();
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

  async createDummyTx(address: string, amountSats: bigint) {
    await this.init();
    const dummyTx = create_dummy_tx(address, amountSats);
    return Promise.resolve(dummyTx);
  }

  async encryptEcies(msg: Uint8Array, publicKey: Uint8Array) {
    await this.init();
    const encryptedMsg = encrypt_ecies(msg, publicKey);
    return Promise.resolve(encryptedMsg);
  }

  async decryptEcies(encryptedMsg: Uint8Array, privateKey: Uint8Array) {
    await this.init();
    const plaintext = decrypt_ecies(encryptedMsg, privateKey);
    return Promise.resolve(plaintext);
  }

  async splitSecretWithProofs(
    secret: Uint8Array,
    threshold: number,
    numShares: number,
  ) {
    await this.init();
    const result = split_secret_with_proofs(secret, threshold, numShares);
    return (
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
    );
  }

  async recoverSecret(
    shares: { threshold: number; index: number; share: Uint8Array }[],
  ) {
    await this.init();
    return recover_secret_wasm(shares);
  }

  async validateShare(
    share: Uint8Array,
    index: number,
    threshold: number,
    proofs: Uint8Array[],
  ) {
    await this.init();
    validate_share(share, index, threshold, proofs);
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

export { type DummyTx, SparkFrostBrowser as SparkFrost };
