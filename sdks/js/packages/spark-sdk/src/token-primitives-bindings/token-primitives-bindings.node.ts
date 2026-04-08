import {
  build_broadcast_transaction_request,
  construct_partial_transfer_transaction,
  finalize_token_invoice,
  hash_partial_token_transaction,
  prepare_token_invoice,
} from "./wasm/wasm-nodejs.js";
import type {
  BroadcastBuildRequestBindingParams,
  FinalizeTokenInvoiceRequestBindingParams,
  PrepareTokenInvoiceRequestBindingParams,
  TransferBuildRequestBindingParams,
} from "./types.js";
import { SparkTokenPrimitivesBase } from "./token-primitives-bindings.js";

class SparkTokenPrimitivesNodeJS extends SparkTokenPrimitivesBase {
  async constructPartialTransferTransaction(
    request: TransferBuildRequestBindingParams,
  ) {
    return construct_partial_transfer_transaction(request);
  }

  async hashPartialTokenTransaction(partialTokenTransactionBytes: Uint8Array) {
    return hash_partial_token_transaction(partialTokenTransactionBytes);
  }

  async buildBroadcastTransactionRequest(
    request: BroadcastBuildRequestBindingParams,
  ) {
    return build_broadcast_transaction_request(request);
  }

  async prepareTokenInvoice(request: PrepareTokenInvoiceRequestBindingParams) {
    return prepare_token_invoice(request);
  }

  async finalizeTokenInvoice(
    request: FinalizeTokenInvoiceRequestBindingParams,
  ) {
    return finalize_token_invoice(request);
  }
}

export { SparkTokenPrimitivesNodeJS as SparkTokenPrimitives };
