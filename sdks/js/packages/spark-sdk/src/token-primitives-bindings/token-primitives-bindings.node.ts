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
  PartialTransferBuildResultBinding,
  PreparedTokenInvoiceBinding,
  PrepareTokenInvoiceRequestBindingParams,
  TransferBuildRequestBindingParams,
} from "./types.js";
import { SparkTokenPrimitivesBase } from "./token-primitives-bindings.js";

class SparkTokenPrimitivesNodeJS extends SparkTokenPrimitivesBase {
  constructPartialTransferTransaction(
    request: TransferBuildRequestBindingParams,
  ): Promise<PartialTransferBuildResultBinding> {
    return new Promise((resolve) => {
      resolve(
        construct_partial_transfer_transaction(
          request,
        ) as PartialTransferBuildResultBinding,
      );
    });
  }

  hashPartialTokenTransaction(
    partialTokenTransactionBytes: Uint8Array,
  ): Promise<Uint8Array> {
    return new Promise((resolve) => {
      resolve(hash_partial_token_transaction(partialTokenTransactionBytes));
    });
  }

  buildBroadcastTransactionRequest(
    request: BroadcastBuildRequestBindingParams,
  ): Promise<Uint8Array> {
    return new Promise((resolve) => {
      resolve(build_broadcast_transaction_request(request));
    });
  }

  prepareTokenInvoice(
    request: PrepareTokenInvoiceRequestBindingParams,
  ): Promise<PreparedTokenInvoiceBinding> {
    return new Promise((resolve) => {
      resolve(prepare_token_invoice(request) as PreparedTokenInvoiceBinding);
    });
  }

  finalizeTokenInvoice(
    request: FinalizeTokenInvoiceRequestBindingParams,
  ): Promise<string> {
    return new Promise((resolve) => {
      resolve(finalize_token_invoice(request));
    });
  }
}

export { SparkTokenPrimitivesNodeJS as SparkTokenPrimitives };
