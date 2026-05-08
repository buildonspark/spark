declare module "@buildonspark/spark-frost-bare-addon" {
  import type {
    ISigningCommitment,
    ISigningNonce,
    IKeyPackage,
  } from "@buildonspark/spark-sdk/bare";

  export type NativeDummyTx = {
    tx: Uint8Array;
    txid: string;
  };

  export type NativeVerifiableSecretShare = {
    threshold: number;
    index: number;
    share: Uint8Array;
    proofs: Uint8Array[];
  };

  export type NativeNodeTxPair = {
    cpfp: { tx: Uint8Array };
    direct: { tx: Uint8Array };
  };

  export type NativeRefundTxTrio = {
    cpfp_refund: { tx: Uint8Array };
    direct_refund?: { tx: Uint8Array };
    direct_from_cpfp_refund: { tx: Uint8Array };
  };

  export function createDummyTx(
    address: string,
    amountSats: bigint,
  ): NativeDummyTx;

  export function encryptEcies(
    msg: Uint8Array,
    publicKey: Uint8Array,
  ): Uint8Array;

  export function decryptEcies(
    encryptedMsg: Uint8Array,
    privateKey: Uint8Array,
  ): Uint8Array;

  export function signFrost(
    message: Uint8Array,
    keyPackage: IKeyPackage,
    nonce: ISigningNonce,
    selfCommitment: ISigningCommitment,
    statechainCommitments: [string, ISigningCommitment][],
    adaptorPubKey: Uint8Array,
  ): Uint8Array;

  export function aggregateFrost(
    message: Uint8Array,
    statechainCommitments: [string, ISigningCommitment][],
    selfCommitment: ISigningCommitment,
    statechainSignatures: [string, Uint8Array][],
    selfSignature: Uint8Array,
    statechainPublicKeys: [string, Uint8Array][],
    selfPublicKey: Uint8Array,
    verifyingKey: Uint8Array,
    adaptorPubKey: Uint8Array,
  ): Uint8Array;

  export function splitSecretWithProofs(
    secret: Uint8Array,
    threshold: number,
    numShares: number,
  ): NativeVerifiableSecretShare[];

  export function recoverSecret(
    shares: { threshold: number; index: number; share: Uint8Array }[],
  ): Uint8Array;

  export function validateShare(
    share: Uint8Array,
    index: number,
    threshold: number,
    proofs: Uint8Array[],
  ): void;

  export function constructNodeTxPair(
    parentTx: Uint8Array,
    vout: number,
    address: string,
    sequence: number,
    directSequence: number,
    feeSats: bigint,
  ): NativeNodeTxPair;

  export function constructRefundTxTrio(
    cpfpNodeTx: Uint8Array,
    directNodeTx: Uint8Array,
    vout: number,
    receivingPubkey: Uint8Array,
    network: string,
    sequence: number,
    directSequence: number,
    feeSats: bigint,
  ): NativeRefundTxTrio;

  export function computeMultiInputSighash(
    tx: Uint8Array,
    inputIndex: number,
    prevOutScripts: Uint8Array[],
    prevOutValues: number[],
  ): Uint8Array;
}
