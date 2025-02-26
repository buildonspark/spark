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

export type InitInput = RequestInfo | URL | Response | BufferSource | WebAssembly.Module;

export interface InitOutput {
  readonly memory: WebAssembly.Memory;
  readonly uniffi_spark_frost_checksum_func_aggregate_frost: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_node_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_refund_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_split_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_create_dummy_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_decrypt_ecies: () => number;
  readonly uniffi_spark_frost_checksum_func_encrypt_ecies: () => number;
  readonly uniffi_spark_frost_checksum_func_frost_nonce: () => number;
  readonly uniffi_spark_frost_checksum_func_sign_frost: () => number;
  readonly ffi_spark_frost_uniffi_contract_version: () => number;
  readonly ffi_spark_frost_rustbuffer_alloc: (a: number, b: bigint, c: number) => void;
  readonly ffi_spark_frost_rustbuffer_from_bytes: (a: number, b: number, c: number, d: number) => void;
  readonly ffi_spark_frost_rustbuffer_free: (a: bigint, b: bigint, c: number, d: number, e: number) => void;
  readonly ffi_spark_frost_rustbuffer_reserve: (a: number, b: bigint, c: bigint, d: number, e: number, f: bigint, g: number) => void;
  readonly ffi_spark_frost_rust_future_poll_u8: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_u8: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_u8: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_u8: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_i8: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_i8: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_i8: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_i8: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_u16: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_u16: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_u16: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_u16: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_i16: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_i16: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_i16: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_i16: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_u32: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_u32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_u32: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_u32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_i32: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_i32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_i32: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_i32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_u64: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_u64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_u64: (a: bigint, b: number) => bigint;
  readonly ffi_spark_frost_rust_future_free_u64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_i64: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_i64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_i64: (a: bigint, b: number) => bigint;
  readonly ffi_spark_frost_rust_future_free_i64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_f32: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_f32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_f32: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_f32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_f64: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_f64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_f64: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_f64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_pointer: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_pointer: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_pointer: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_free_pointer: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_rust_buffer: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_rust_buffer: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_rust_buffer: (a: number, b: bigint, c: number) => void;
  readonly ffi_spark_frost_rust_future_free_rust_buffer: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_void: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_void: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_void: (a: bigint, b: number) => void;
  readonly ffi_spark_frost_rust_future_free_void: (a: bigint) => void;
  readonly uniffi_spark_frost_fn_func_aggregate_frost: (a: number, b: bigint, c: bigint, d: number, e: number, f: bigint, g: bigint, h: number, i: number, j: bigint, k: bigint, l: number, m: number, n: bigint, o: bigint, p: number, q: number, r: bigint, s: bigint, t: number, u: number, v: bigint, w: bigint, x: number, y: number, z: bigint, a1: bigint, b1: number, c1: number, d1: bigint, e1: bigint, f1: number, g1: number, h1: bigint, i1: bigint, j1: number, k1: number, l1: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_node_tx: (a: number, b: bigint, c: bigint, d: number, e: number, f: number, g: bigint, h: bigint, i: number, j: number, k: number, l: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_refund_tx: (a: number, b: bigint, c: bigint, d: number, e: number, f: number, g: bigint, h: bigint, i: number, j: number, k: bigint, l: bigint, m: number, n: number, o: number, p: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_split_tx: (a: number, b: bigint, c: bigint, d: number, e: number, f: number, g: bigint, h: bigint, i: number, j: number, k: number, l: number) => void;
  readonly uniffi_spark_frost_fn_func_create_dummy_tx: (a: number, b: bigint, c: bigint, d: number, e: number, f: bigint, g: number) => void;
  readonly uniffi_spark_frost_fn_func_decrypt_ecies: (a: number, b: bigint, c: bigint, d: number, e: number, f: bigint, g: bigint, h: number, i: number, j: number) => void;
  readonly uniffi_spark_frost_fn_func_encrypt_ecies: (a: number, b: bigint, c: bigint, d: number, e: number, f: bigint, g: bigint, h: number, i: number, j: number) => void;
  readonly uniffi_spark_frost_fn_func_frost_nonce: (a: number, b: bigint, c: bigint, d: number, e: number, f: number) => void;
  readonly uniffi_spark_frost_fn_func_sign_frost: (a: number, b: bigint, c: bigint, d: number, e: number, f: bigint, g: bigint, h: number, i: number, j: bigint, k: bigint, l: number, m: number, n: bigint, o: bigint, p: number, q: number, r: bigint, s: bigint, t: number, u: number, v: bigint, w: bigint, x: number, y: number, z: number) => void;
  readonly __wbg_signingnonce_free: (a: number, b: number) => void;
  readonly __wbg_signingcommitment_free: (a: number, b: number) => void;
  readonly signingcommitment_new: (a: number, b: number, c: number, d: number) => number;
  readonly __wbg_nonceresult_free: (a: number, b: number) => void;
  readonly __wbg_get_nonceresult_nonce: (a: number) => number;
  readonly __wbg_set_nonceresult_nonce: (a: number, b: number) => void;
  readonly __wbg_get_nonceresult_commitment: (a: number) => number;
  readonly __wbg_set_nonceresult_commitment: (a: number, b: number) => void;
  readonly __wbg_keypackage_free: (a: number, b: number) => void;
  readonly __wbg_get_keypackage_public_key: (a: number) => [number, number];
  readonly __wbg_get_keypackage_verifying_key: (a: number) => [number, number];
  readonly __wbg_set_keypackage_verifying_key: (a: number, b: number, c: number) => void;
  readonly keypackage_new: (a: number, b: number, c: number, d: number, e: number, f: number) => number;
  readonly frost_nonce: (a: number) => [number, number, number];
  readonly wasm_sign_frost: (a: number, b: number, c: number, d: number, e: number, f: any, g: number, h: number) => [number, number, number, number];
  readonly wasm_aggregate_frost: (a: number, b: number, c: any, d: number, e: any, f: number, g: number, h: any, i: number, j: number, k: number, l: number, m: number, n: number) => [number, number, number, number];
  readonly __wbg_transactionresult_free: (a: number, b: number) => void;
  readonly construct_node_tx: (a: number, b: number, c: number, d: number, e: number, f: number) => [number, number, number];
  readonly construct_refund_tx: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number) => [number, number, number];
  readonly construct_split_tx: (a: number, b: number, c: number, d: number, e: number, f: number) => [number, number, number];
  readonly __wbg_dummytx_free: (a: number, b: number) => void;
  readonly __wbg_get_dummytx_tx: (a: number) => [number, number];
  readonly __wbg_set_dummytx_tx: (a: number, b: number, c: number) => void;
  readonly __wbg_get_dummytx_txid: (a: number) => [number, number];
  readonly __wbg_set_dummytx_txid: (a: number, b: number, c: number) => void;
  readonly create_dummy_tx: (a: number, b: number, c: bigint) => [number, number, number];
  readonly encrypt_ecies: (a: number, b: number, c: number, d: number) => [number, number, number, number];
  readonly decrypt_ecies: (a: number, b: number, c: number, d: number) => [number, number, number, number];
  readonly signingnonce_new: (a: number, b: number, c: number, d: number) => number;
  readonly __wbg_set_signingnonce_hiding: (a: number, b: number, c: number) => void;
  readonly __wbg_set_signingnonce_binding: (a: number, b: number, c: number) => void;
  readonly __wbg_set_signingcommitment_hiding: (a: number, b: number, c: number) => void;
  readonly __wbg_set_signingcommitment_binding: (a: number, b: number, c: number) => void;
  readonly __wbg_set_transactionresult_tx: (a: number, b: number, c: number) => void;
  readonly __wbg_set_transactionresult_sighash: (a: number, b: number, c: number) => void;
  readonly __wbg_set_keypackage_secret_key: (a: number, b: number, c: number) => void;
  readonly __wbg_set_keypackage_public_key: (a: number, b: number, c: number) => void;
  readonly __wbg_get_signingnonce_hiding: (a: number) => [number, number];
  readonly __wbg_get_signingnonce_binding: (a: number) => [number, number];
  readonly __wbg_get_signingcommitment_hiding: (a: number) => [number, number];
  readonly __wbg_get_signingcommitment_binding: (a: number) => [number, number];
  readonly __wbg_get_transactionresult_tx: (a: number) => [number, number];
  readonly __wbg_get_transactionresult_sighash: (a: number) => [number, number];
  readonly __wbg_get_keypackage_secret_key: (a: number) => [number, number];
  readonly rustsecp256k1_v0_10_0_context_create: (a: number) => number;
  readonly rustsecp256k1_v0_10_0_context_destroy: (a: number) => void;
  readonly rustsecp256k1_v0_10_0_default_illegal_callback_fn: (a: number, b: number) => void;
  readonly rustsecp256k1_v0_10_0_default_error_callback_fn: (a: number, b: number) => void;
  readonly __wbindgen_malloc: (a: number, b: number) => number;
  readonly __wbindgen_realloc: (a: number, b: number, c: number, d: number) => number;
  readonly __wbindgen_exn_store: (a: number) => void;
  readonly __externref_table_alloc: () => number;
  readonly __wbindgen_export_4: WebAssembly.Table;
  readonly __wbindgen_free: (a: number, b: number, c: number) => void;
  readonly __externref_table_dealloc: (a: number) => void;
  readonly __wbindgen_start: () => void;
}

export type SyncInitInput = BufferSource | WebAssembly.Module;
/**
* Instantiates the given `module`, which can either be bytes or
* a precompiled `WebAssembly.Module`.
*
* @param {{ module: SyncInitInput }} module - Passing `SyncInitInput` directly is deprecated.
*
* @returns {InitOutput}
*/
export function initSync(module: { module: SyncInitInput } | SyncInitInput): InitOutput;

/**
* If `module_or_path` is {RequestInfo} or {URL}, makes a request and
* for everything else, calls `WebAssembly.instantiate` directly.
*
* @param {{ module_or_path: InitInput | Promise<InitInput> }} module_or_path - Passing `InitInput` directly is deprecated.
*
* @returns {Promise<InitOutput>}
*/
export default function __wbg_init (module_or_path?: { module_or_path: InitInput | Promise<InitInput> } | InitInput | Promise<InitInput>): Promise<InitOutput>;
