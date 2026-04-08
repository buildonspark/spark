import type {
  BroadcastBuildRequestBindingParams,
  FinalizeTokenInvoiceRequestBindingParams,
  PartialTransferBuildResultBinding,
  PreparedTokenInvoiceBinding,
  PrepareTokenInvoiceRequestBindingParams,
  TransferBuildRequestBindingParams,
} from "./types.js";

export abstract class SparkTokenPrimitivesBase {
  abstract constructPartialTransferTransaction(
    request: TransferBuildRequestBindingParams,
  ): Promise<PartialTransferBuildResultBinding>;
  abstract hashPartialTokenTransaction(
    partialTokenTransactionBytes: Uint8Array,
  ): Promise<Uint8Array>;
  abstract buildBroadcastTransactionRequest(
    request: BroadcastBuildRequestBindingParams,
  ): Promise<Uint8Array>;
  abstract prepareTokenInvoice(
    request: PrepareTokenInvoiceRequestBindingParams,
  ): Promise<PreparedTokenInvoiceBinding>;
  abstract finalizeTokenInvoice(
    request: FinalizeTokenInvoiceRequestBindingParams,
  ): Promise<string>;
}

let sparkTokenPrimitives: SparkTokenPrimitivesBase | null = null;

export function setSparkTokenPrimitivesOnce(
  sparkTokenPrimitivesParam: SparkTokenPrimitivesBase,
) {
  if (sparkTokenPrimitives) {
    return;
  }
  sparkTokenPrimitives = sparkTokenPrimitivesParam;
}

export function getSparkTokenPrimitives() {
  if (!sparkTokenPrimitives) {
    throw new Error("sparkTokenPrimitives is not set");
  }
  return sparkTokenPrimitives;
}

export function getSparkTokenPrimitivesOrNull() {
  return sparkTokenPrimitives;
}
