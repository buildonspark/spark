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
}

export { type DummyTx, SparkFrostBrowser as SparkFrost };
