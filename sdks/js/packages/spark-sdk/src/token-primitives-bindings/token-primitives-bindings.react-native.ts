import type {
  BroadcastBuildRequestBindingParams,
  FinalizeTokenInvoiceRequestBindingParams,
  PartialTransferBuildResultBinding,
  PreparedTokenInvoiceBinding,
  PrepareTokenInvoiceRequestBindingParams,
  TransferBuildRequestBindingParams,
} from "./types.js";
import { SparkTokenPrimitivesBase } from "./token-primitives-bindings.js";
import { NativeModules } from "react-native";

type NativePartialTransferBuildResult = {
  partialTokenTransactionBytes: number[];
  partialTokenTransactionHash: number[];
};

type NativePreparedTokenInvoice = {
  sparkInvoiceFieldsBytes: number[];
  sparkInvoiceHash: number[];
  unsignedSparkAddress: string;
};

type SparkTokenPrimitivesNativeModule = {
  constructPartialTransferTransaction(
    params: unknown,
  ): Promise<NativePartialTransferBuildResult>;
  hashPartialTokenTransaction(params: unknown): Promise<number[]>;
  buildBroadcastTransactionRequest(params: unknown): Promise<number[]>;
  prepareTokenInvoice(params: unknown): Promise<NativePreparedTokenInvoice>;
  finalizeTokenInvoice(params: unknown): Promise<string>;
};

const SparkTokenPrimitivesModule = NativeModules.SparkTokenPrimitivesModule as
  | SparkTokenPrimitivesNativeModule
  | undefined;

function getModule(): SparkTokenPrimitivesNativeModule {
  if (!SparkTokenPrimitivesModule) {
    throw new Error(
      "SparkTokenPrimitivesModule is not available in this environment",
    );
  }
  return SparkTokenPrimitivesModule;
}

const toNumberArray = (arr: Uint8Array): number[] => Array.from(arr);
const toUint8Array = (arr: number[]): Uint8Array => new Uint8Array(arr);
const toOptionalNumberArray = (arr: Uint8Array | undefined): number[] | null =>
  arr ? toNumberArray(arr) : null;

class SparkTokenPrimitivesReactNative extends SparkTokenPrimitivesBase {
  async constructPartialTransferTransaction(
    request: TransferBuildRequestBindingParams,
  ): Promise<PartialTransferBuildResultBinding> {
    const nativeParams = {
      identityPublicKey: toNumberArray(request.identityPublicKey),
      selectedOutputs: request.selectedOutputs.map((o) => ({
        previousTransactionHash: toNumberArray(o.previousTransactionHash),
        previousTransactionVout: o.previousTransactionVout,
        ownerPublicKey: toNumberArray(o.ownerPublicKey),
        tokenIdentifier: toNumberArray(o.tokenIdentifier),
        tokenAmount: toNumberArray(o.tokenAmount),
      })),
      receiverOutputs: request.receiverOutputs.map((o) => ({
        receiverSparkAddress: o.receiverSparkAddress,
        tokenIdentifier: toOptionalNumberArray(o.tokenIdentifier),
        tokenAmount: toOptionalNumberArray(o.tokenAmount),
      })),
      operatorIdentityPublicKeys:
        request.operatorIdentityPublicKeys.map(toNumberArray),
      network: request.network,
      validityDurationSeconds: request.validityDurationSeconds,
      clientCreatedTimestampUnixMicros:
        request.clientCreatedTimestampUnixMicros,
      withdrawBondSats: request.withdrawBondSats,
      withdrawRelativeBlockLocktime: request.withdrawRelativeBlockLocktime,
      executeBeforeUnixMicros: request.executeBeforeUnixMicros ?? null,
    };
    const result =
      await getModule().constructPartialTransferTransaction(nativeParams);
    return {
      partialTokenTransactionBytes: toUint8Array(
        result.partialTokenTransactionBytes,
      ),
      partialTokenTransactionHash: toUint8Array(
        result.partialTokenTransactionHash,
      ),
    };
  }

  async hashPartialTokenTransaction(
    partialTokenTransactionBytes: Uint8Array,
  ): Promise<Uint8Array> {
    const result = await getModule().hashPartialTokenTransaction({
      partialTokenTransactionBytes: toNumberArray(partialTokenTransactionBytes),
    });
    return toUint8Array(result);
  }

  async buildBroadcastTransactionRequest(
    request: BroadcastBuildRequestBindingParams,
  ): Promise<Uint8Array> {
    const nativeParams = {
      identityPublicKey: toNumberArray(request.identityPublicKey),
      partialTokenTransactionBytes: toNumberArray(
        request.partialTokenTransactionBytes,
      ),
      ownerSignatures: request.ownerSignatures.map((s) => ({
        inputIndex: s.inputIndex,
        publicKey: toNumberArray(s.publicKey),
        signature: toNumberArray(s.signature),
      })),
    };
    const result =
      await getModule().buildBroadcastTransactionRequest(nativeParams);
    return toUint8Array(result);
  }

  async prepareTokenInvoice(
    request: PrepareTokenInvoiceRequestBindingParams,
  ): Promise<PreparedTokenInvoiceBinding> {
    const nativeParams = {
      receiverIdentityPublicKey: toNumberArray(
        request.receiverIdentityPublicKey,
      ),
      network: request.network,
      tokenIdentifier: toOptionalNumberArray(request.tokenIdentifier),
      tokenAmount: toOptionalNumberArray(request.tokenAmount),
      memo: request.memo ?? null,
      senderSparkAddress: request.senderSparkAddress ?? null,
      expiryTimeUnixMillis: request.expiryTimeUnixMillis ?? null,
      invoiceId: toOptionalNumberArray(request.invoiceId),
    };
    const result = await getModule().prepareTokenInvoice(nativeParams);
    return {
      sparkInvoiceFieldsBytes: toUint8Array(result.sparkInvoiceFieldsBytes),
      sparkInvoiceHash: toUint8Array(result.sparkInvoiceHash),
      unsignedSparkAddress: result.unsignedSparkAddress,
    };
  }

  async finalizeTokenInvoice(
    request: FinalizeTokenInvoiceRequestBindingParams,
  ): Promise<string> {
    const nativeParams = {
      receiverIdentityPublicKey: toNumberArray(
        request.receiverIdentityPublicKey,
      ),
      network: request.network,
      sparkInvoiceFieldsBytes: toNumberArray(request.sparkInvoiceFieldsBytes),
      signature: toOptionalNumberArray(request.signature),
    };
    return getModule().finalizeTokenInvoice(nativeParams);
  }
}

export { SparkTokenPrimitivesReactNative as SparkTokenPrimitives };
