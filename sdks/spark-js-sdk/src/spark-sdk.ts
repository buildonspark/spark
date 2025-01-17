import { initWasm } from "./utils/wasm-wrapper";

import {
  construct_node_tx,
  construct_refund_tx,
  construct_split_tx,
  create_dummy_tx,
  decrypt_ecies,
  DummyTx,
  encrypt_ecies,
  frost_nonce,
  InitOutput,
  KeyPackage,
  NonceResult,
  SigningCommitment,
  SigningNonce,
  TransactionResult,
  wasm_aggregate_frost,
  wasm_sign_frost,
} from "./wasm/spark_bindings";

type FrostSignParams = {
  msg: Uint8Array;
  keyPackage: KeyPackage;
  nonce: SigningNonce;
  selfCommitment: SigningCommitment;
  statechainCommitments: any;
};

type AggregateFrostParams = {
  msg: Uint8Array;
  statechainCommitments: any;
  selfCommitment: SigningCommitment;
  statechainSignatures: any;
  selfSignature: Uint8Array;
  statechainPublicKeys: any;
  selfPublicKey: Uint8Array;
  verifyingKey: Uint8Array;
};

type ConstructNodeTxParams = {
  tx: Uint8Array;
  vout: number;
  address: string;
  locktime: number;
};

export type Network = "mainnet" | "regtest" | "testnet";

type SigningOperator = {
  id: number;
  identifier: string;
  address: string;
  identityPublicKey: Uint8Array;
};

export class SparkSDK {
  private wasmModule: InitOutput | null = null;

  constructor() {
    this.initAsync();
  }

  private async initAsync() {
    this.wasmModule = await initWasm();
  }

  private async ensureInitialized() {
    if (!this.wasmModule) {
      await this.initAsync();
    }
  }

  private async frostNonce({
    keyPackage,
  }: {
    keyPackage: KeyPackage;
  }): Promise<NonceResult> {
    await this.ensureInitialized();
    return frost_nonce(keyPackage);
  }

  private async signFrost({
    msg,
    keyPackage,
    nonce,
    selfCommitment,
    statechainCommitments,
  }: FrostSignParams): Promise<Uint8Array> {
    await this.ensureInitialized();
    return wasm_sign_frost(
      msg,
      keyPackage,
      nonce,
      selfCommitment,
      statechainCommitments
    );
  }

  private async aggregateFrost({
    msg,
    statechainCommitments,
    selfCommitment,
    statechainSignatures,
    selfSignature,
    statechainPublicKeys,
    selfPublicKey,
    verifyingKey,
  }: AggregateFrostParams): Promise<Uint8Array> {
    await this.ensureInitialized();
    return wasm_aggregate_frost(
      msg,
      statechainCommitments,
      selfCommitment,
      statechainSignatures,
      selfSignature,
      statechainPublicKeys,
      selfPublicKey,
      verifyingKey
    );
  }

  private async constructNodeTx({
    tx,
    vout,
    address,
    locktime,
  }: ConstructNodeTxParams): Promise<TransactionResult> {
    await this.ensureInitialized();
    return construct_node_tx(tx, vout, address, locktime);
  }

  private async constructRefundTx({
    tx,
    vout,
    pubkey,
    network,
    locktime,
  }: {
    tx: Uint8Array;
    vout: number;
    pubkey: Uint8Array;
    network: string;
    locktime: number;
  }): Promise<TransactionResult> {
    await this.ensureInitialized();
    return construct_refund_tx(tx, vout, pubkey, network, locktime);
  }

  private async constructSplitTx({
    tx,
    vout,
    addresses,
    locktime,
  }: {
    tx: Uint8Array;
    vout: number;
    addresses: string[];
    locktime: number;
  }): Promise<TransactionResult> {
    await this.ensureInitialized();
    return construct_split_tx(tx, vout, addresses, locktime);
  }

  private async createDummyTx({
    address,
    amountSats,
  }: {
    address: string;
    amountSats: bigint;
  }): Promise<DummyTx> {
    await this.ensureInitialized();
    return create_dummy_tx(address, amountSats);
  }

  private async encryptEcies({
    msg,
    publicKey,
  }: {
    msg: Uint8Array;
    publicKey: Uint8Array;
  }): Promise<Uint8Array> {
    await this.ensureInitialized();
    return encrypt_ecies(msg, publicKey);
  }

  private async decryptEcies({
    encryptedMsg,
    privateKey,
  }: {
    encryptedMsg: Uint8Array;
    privateKey: Uint8Array;
  }): Promise<Uint8Array> {
    await this.ensureInitialized();
    return decrypt_ecies(encryptedMsg, privateKey);
  }
}
