import {
  build_broadcast_transaction_request,
  construct_partial_transfer_transaction,
  default as initWasm,
  finalize_token_invoice,
  hash_partial_token_transaction,
  InitOutput,
  prepare_token_invoice,
} from "./wasm/wasm-browser.js";
import type {
  BroadcastBuildRequestBindingParams,
  FinalizeTokenInvoiceRequestBindingParams,
  PrepareTokenInvoiceRequestBindingParams,
  TransferBuildRequestBindingParams,
} from "./types.js";
import { SparkTokenPrimitivesBase } from "./token-primitives-bindings.js";
import wasmBytes from "./wasm/wasm-browser-bg.wasm";

class SparkTokenPrimitivesBrowser extends SparkTokenPrimitivesBase {
  private static initPromise: Promise<InitOutput> | null = null;
  private static initialized = false;
  private static initError: Error | null = null;

  private async init(): Promise<void> {
    if (SparkTokenPrimitivesBrowser.initialized) {
      return;
    }

    if (SparkTokenPrimitivesBrowser.initError) {
      throw new Error(
        `SparkTokenPrimitives: WASM module failed to initialize: ${SparkTokenPrimitivesBrowser.initError.message}`,
      );
    }

    if (SparkTokenPrimitivesBrowser.initPromise) {
      await SparkTokenPrimitivesBrowser.initPromise;
      return;
    }

    SparkTokenPrimitivesBrowser.initPromise = (async () => {
      try {
        const result = await initWasm({ module_or_path: wasmBytes });
        SparkTokenPrimitivesBrowser.initialized = true;
        return result;
      } catch (err) {
        console.error("SparkTokenPrimitives: WASM initialization failed:", err);
        SparkTokenPrimitivesBrowser.initPromise = null;
        SparkTokenPrimitivesBrowser.initError =
          err instanceof Error ? err : new Error(String(err));
        throw SparkTokenPrimitivesBrowser.initError;
      }
    })();

    await SparkTokenPrimitivesBrowser.initPromise;
  }

  async constructPartialTransferTransaction(
    request: TransferBuildRequestBindingParams,
  ) {
    await this.init();
    return construct_partial_transfer_transaction(request);
  }

  async hashPartialTokenTransaction(partialTokenTransactionBytes: Uint8Array) {
    await this.init();
    return hash_partial_token_transaction(partialTokenTransactionBytes);
  }

  async buildBroadcastTransactionRequest(
    request: BroadcastBuildRequestBindingParams,
  ) {
    await this.init();
    return build_broadcast_transaction_request(request);
  }

  async prepareTokenInvoice(request: PrepareTokenInvoiceRequestBindingParams) {
    await this.init();
    return prepare_token_invoice(request);
  }

  async finalizeTokenInvoice(
    request: FinalizeTokenInvoiceRequestBindingParams,
  ) {
    await this.init();
    return finalize_token_invoice(request);
  }
}

export { SparkTokenPrimitivesBrowser as SparkTokenPrimitives };
