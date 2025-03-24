/* tslint:disable */
/* eslint-disable */
export function frost_nonce(key_package: KeyPackage): NonceResult;
export function wasm_sign_frost(msg: Uint8Array, key_package: KeyPackage, nonce: SigningNonce, self_commitment: SigningCommitment, statechain_commitments: any, adaptor_public_key?: Uint8Array | null): Uint8Array;
export function wasm_aggregate_frost(msg: Uint8Array, statechain_commitments: any, self_commitment: SigningCommitment, statechain_signatures: any, self_signature: Uint8Array, statechain_public_keys: any, self_public_key: Uint8Array, verifying_key: Uint8Array, adaptor_public_key?: Uint8Array | null): Uint8Array;
export function construct_node_tx(tx: Uint8Array, vout: number, address: string, locktime: number): TransactionResult;
export function construct_refund_tx(tx: Uint8Array, vout: number, pubkey: Uint8Array, network: string, locktime: number): TransactionResult;
export function construct_split_tx(tx: Uint8Array, vout: number, addresses: string[], locktime: number): TransactionResult;
export function create_dummy_tx(address: string, amount_sats: bigint): DummyTx;
export function encrypt_ecies(msg: Uint8Array, public_key_bytes: Uint8Array): Uint8Array;
export function decrypt_ecies(encrypted_msg: Uint8Array, private_key_bytes: Uint8Array): Uint8Array;
export class DummyTx {
  private constructor();
  free(): void;
  tx: Uint8Array;
  txid: string;
}
export class KeyPackage {
  free(): void;
  constructor(secret_key: Uint8Array, public_key: Uint8Array, verifying_key: Uint8Array);
  secret_key: Uint8Array;
  public_key: Uint8Array;
  verifying_key: Uint8Array;
}
export class NonceResult {
  private constructor();
  free(): void;
  nonce: SigningNonce;
  commitment: SigningCommitment;
}
export class SigningCommitment {
  free(): void;
  constructor(hiding: Uint8Array, binding: Uint8Array);
  hiding: Uint8Array;
  binding: Uint8Array;
}
export class SigningNonce {
  free(): void;
  constructor(hiding: Uint8Array, binding: Uint8Array);
  hiding: Uint8Array;
  binding: Uint8Array;
}
export class TransactionResult {
  private constructor();
  free(): void;
  tx: Uint8Array;
  sighash: Uint8Array;
}
