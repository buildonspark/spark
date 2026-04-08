export interface SelectedTokenOutputBindingParams {
  previousTransactionHash: Uint8Array;
  previousTransactionVout: number;
  ownerPublicKey: Uint8Array;
  tokenIdentifier: Uint8Array;
  tokenAmount: Uint8Array;
}

export interface ReceiverTokenOutputBindingParams {
  receiverSparkAddress: string;
  tokenIdentifier?: Uint8Array;
  tokenAmount?: Uint8Array;
}

export interface TransferBuildRequestBindingParams {
  identityPublicKey: Uint8Array;
  selectedOutputs: SelectedTokenOutputBindingParams[];
  receiverOutputs: ReceiverTokenOutputBindingParams[];
  operatorIdentityPublicKeys: Uint8Array[];
  network: number;
  validityDurationSeconds: number;
  clientCreatedTimestampUnixMicros: number;
  withdrawBondSats: number;
  withdrawRelativeBlockLocktime: number;
}

export interface PartialTransferBuildResultBinding {
  partialTokenTransactionBytes: Uint8Array;
  partialTokenTransactionHash: Uint8Array;
}

export interface SignatureWithIndexBindingParams {
  inputIndex: number;
  publicKey: Uint8Array;
  signature: Uint8Array;
}

export interface BroadcastBuildRequestBindingParams {
  identityPublicKey: Uint8Array;
  partialTokenTransactionBytes: Uint8Array;
  ownerSignatures: SignatureWithIndexBindingParams[];
}

export interface PrepareTokenInvoiceRequestBindingParams {
  receiverIdentityPublicKey: Uint8Array;
  network: number;
  tokenIdentifier?: Uint8Array;
  tokenAmount?: Uint8Array;
  memo?: string;
  senderSparkAddress?: string;
  expiryTimeUnixMillis?: number;
  invoiceId?: Uint8Array;
}

export interface PreparedTokenInvoiceBinding {
  sparkInvoiceFieldsBytes: Uint8Array;
  sparkInvoiceHash: Uint8Array;
  unsignedSparkAddress: string;
}

export interface FinalizeTokenInvoiceRequestBindingParams {
  receiverIdentityPublicKey: Uint8Array;
  network: number;
  sparkInvoiceFieldsBytes: Uint8Array;
  signature?: Uint8Array;
}
