import {
  construct_node_tx,
  construct_refund_tx,
  construct_split_tx,
  create_dummy_tx,
  decrypt_ecies,
  DummyTx,
  encrypt_ecies,
  KeyPackage,
  TransactionResult,
  wasm_aggregate_frost,
  wasm_sign_frost,
  SigningNonce,
  SigningCommitment,
} from "../wasm/spark_bindings";

export type FrostSignParams = {
  msg: Uint8Array;
  keyPackage: KeyPackage;
  nonce: SigningNonce;
  selfCommitment: SigningCommitment;
  statechainCommitments: any;
};

export type AggregateFrostParams = {
  msg: Uint8Array;
  statechainCommitments: any;
  selfCommitment: SigningCommitment;
  statechainSignatures: any;
  selfSignature: Uint8Array;
  statechainPublicKeys: any;
  selfPublicKey: Uint8Array;
  verifyingKey: Uint8Array;
};

export type ConstructNodeTxParams = {
  tx: Uint8Array;
  vout: number;
  address: string;
  locktime: number;
};

export function signFrost({
  msg,
  keyPackage,
  nonce,
  selfCommitment,
  statechainCommitments,
}: FrostSignParams): Uint8Array {
  return wasm_sign_frost(
    msg,
    keyPackage,
    nonce,
    selfCommitment,
    statechainCommitments
  );
}

export function aggregateFrost({
  msg,
  statechainCommitments,
  selfCommitment,
  statechainSignatures,
  selfSignature,
  statechainPublicKeys,
  selfPublicKey,
  verifyingKey,
}: AggregateFrostParams): Uint8Array {
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

export function constructNodeTx({
  tx,
  vout,
  address,
  locktime,
}: ConstructNodeTxParams): TransactionResult {
  return construct_node_tx(tx, vout, address, locktime);
}

export function constructRefundTx({
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
}): TransactionResult {
  return construct_refund_tx(tx, vout, pubkey, network, locktime);
}

export function constructSplitTx({
  tx,
  vout,
  addresses,
  locktime,
}: {
  tx: Uint8Array;
  vout: number;
  addresses: string[];
  locktime: number;
}): TransactionResult {
  return construct_split_tx(tx, vout, addresses, locktime);
}

export function createDummyTx({
  address,
  amountSats,
}: {
  address: string;
  amountSats: bigint;
}): DummyTx {
  return create_dummy_tx(address, amountSats);
}

export function encryptEcies({
  msg,
  publicKey,
}: {
  msg: Uint8Array;
  publicKey: Uint8Array;
}): Uint8Array {
  return encrypt_ecies(msg, publicKey);
}

export function decryptEcies({
  encryptedMsg,
  privateKey,
}: {
  encryptedMsg: Uint8Array;
  privateKey: Uint8Array;
}): Uint8Array {
  return decrypt_ecies(encryptedMsg, privateKey);
}
