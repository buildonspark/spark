export interface IKeyPackage {
  secretKey: Uint8Array;
  publicKey: Uint8Array;
  verifyingKey: Uint8Array;
}

export interface ISigningNonce {
  hiding: Uint8Array;
  binding: Uint8Array;
}

export interface ISigningCommitment {
  hiding: Uint8Array;
  binding: Uint8Array;
}

export interface DummyTx {
  tx: Uint8Array;
  txid: string;
}

export type SignFrostParams = {
  message: Uint8Array;
  keyPackage: IKeyPackage;
  nonce: ISigningNonce;
  selfCommitment: ISigningCommitment;
  statechainCommitments: { [key: string]: ISigningCommitment } | undefined;
  adaptorPubKey?: Uint8Array;
};

export type AggregateFrostParams = {
  message: Uint8Array;
  statechainCommitments: { [key: string]: ISigningCommitment } | undefined;
  selfCommitment: ISigningCommitment;
  statechainSignatures: { [key: string]: Uint8Array } | undefined;
  selfSignature: Uint8Array;
  statechainPublicKeys: { [key: string]: Uint8Array } | undefined;
  selfPublicKey: Uint8Array;
  verifyingKey: Uint8Array;
  adaptorPubKey?: Uint8Array;
};

export type ConstructNodeTxParams = {
  tx: Uint8Array;
  vout: number;
  address: string;
  locktime: number;
};
