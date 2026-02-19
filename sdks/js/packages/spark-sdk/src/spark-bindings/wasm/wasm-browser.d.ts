/* tslint:disable */
/* eslint-disable */
export function get_public_key_bytes(private_key_bytes: Uint8Array, compressed: boolean): Uint8Array;
export function compute_multi_input_sighash(tx: Uint8Array, input_index: number, prev_out_scripts: any, prev_out_values: any): Uint8Array;
export function construct_htlc_sender_spend(htlc_tx: Uint8Array, destination_pubkey: Uint8Array, payment_hash: Uint8Array, hashlock_pubkey: Uint8Array, seqlock_pubkey: Uint8Array, htlc_sequence: number, fee_sats: bigint, network: string): HTLCSpendResult;
export function apply_adaptor_to_signature(pub_key: Uint8Array, hash: Uint8Array, signature: Uint8Array, adaptor_private_key: Uint8Array): Uint8Array;
export function construct_htlc_transaction(node_tx: Uint8Array, vout: number, sequence: number, payment_hash: Uint8Array, hashlock_pubkey: Uint8Array, seqlock_pubkey: Uint8Array, htlc_sequence: number, apply_fee: boolean, fee_sats: bigint, network: string): TransactionResult;
export function verify_signature_bytes(signature: Uint8Array, message: Uint8Array, public_key: Uint8Array): boolean;
export function construct_htlc_receiver_spend(htlc_tx: Uint8Array, destination_pubkey: Uint8Array, payment_hash: Uint8Array, hashlock_pubkey: Uint8Array, seqlock_pubkey: Uint8Array, htlc_sequence: number, fee_sats: bigint, network: string): HTLCSpendResult;
export function random_secret_key_bytes(): Uint8Array;
export function next_sequence(curr_sequence: number, time_lock_interval: number, direct_timelock_offset: number): TimelockResult;
export function split_secret(secret: Uint8Array, threshold: number, num_shares: number): SecretShareResult[];
export function generate_adaptor_from_signature(signature: Uint8Array): AdaptorSignatureResult;
export function validate_share(share: Uint8Array, index: number, threshold: number, proofs: any): void;
export function generate_signature_from_existing_adaptor(signature: Uint8Array, adaptor_private_key: Uint8Array): Uint8Array;
export function get_timelock_from_sequence(sequence: number): number;
export function split_secret_with_proofs(secret: Uint8Array, threshold: number, num_shares: number): any;
export function decrypt_ecies(encrypted_msg: Uint8Array, private_key_bytes: Uint8Array): Uint8Array;
export function construct_split_tx(tx: Uint8Array, vout: number, addresses: string[], locktime: number): TransactionResult;
export function validate_adaptor_signature(pub_key: Uint8Array, hash: Uint8Array, signature: Uint8Array, adaptor_pub_key: Uint8Array): void;
export function wasm_aggregate_frost(msg: Uint8Array, statechain_commitments: any, self_commitment: SigningCommitment, statechain_signatures: any, self_signature: Uint8Array, statechain_public_keys: any, self_public_key: Uint8Array, verifying_key: Uint8Array, adaptor_public_key?: Uint8Array | null): Uint8Array;
export function wasm_sign_frost(msg: Uint8Array, key_package: KeyPackage, nonce: SigningNonce, self_commitment: SigningCommitment, statechain_commitments: any, adaptor_public_key?: Uint8Array | null): Uint8Array;
export function check_if_valid_sequence(sequence: number): void;
export function round_down_to_timelock_interval(timelock: number, time_lock_interval: number): number;
export function construct_refund_tx_trio(cpfp_node_tx: Uint8Array, direct_node_tx: Uint8Array | null | undefined, vout: number, receiving_pubkey: Uint8Array, network: string, sequence: number, direct_sequence: number, fee_sats: bigint): RefundTxTrioResult;
export function construct_direct_refund_tx(tx: Uint8Array, vout: number, pubkey: Uint8Array, network: string, sequence: number): TransactionResult;
export function construct_node_tx(tx: Uint8Array, vout: number, address: string, locktime: number): TransactionResult;
export function recover_secret_wasm(shares: any): Uint8Array;
export function construct_node_tx_pair(parent_tx: Uint8Array, vout: number, address: string, sequence: number, direct_sequence: number, fee_sats: bigint): NodeTxPairResult;
export function construct_refund_tx(tx: Uint8Array, vout: number, pubkey: Uint8Array, network: string, sequence: number): TransactionResult;
export function is_zero_timelock(sequence: number): boolean;
export function encrypt_ecies(msg: Uint8Array, public_key_bytes: Uint8Array): Uint8Array;
export function frost_nonce(key_package: KeyPackage): NonceResult;
export function create_dummy_tx(address: string, amount_sats: bigint): DummyTx;
export function get_taproot_pubkey(verifying_pubkey: Uint8Array): Uint8Array;
export class AdaptorSignatureResult {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  signature: Uint8Array;
  adaptor_private_key: Uint8Array;
}
export class DummyTx {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  tx: Uint8Array;
  txid: string;
}
export class HTLCSpendResult {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  tx: Uint8Array;
  sighash: Uint8Array;
  script: Uint8Array;
  control_block: Uint8Array;
}
export class KeyPackage {
  free(): void;
  [Symbol.dispose](): void;
  constructor(secret_key: Uint8Array, public_key: Uint8Array, verifying_key: Uint8Array);
  secret_key: Uint8Array;
  public_key: Uint8Array;
  verifying_key: Uint8Array;
}
export class NodeTxPairResult {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  cpfp: TransactionResult;
  direct: TransactionResult;
}
export class NonceResult {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  nonce: SigningNonce;
  commitment: SigningCommitment;
}
export class RefundTxTrioResult {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  cpfp_refund: TransactionResult;
  get direct_refund(): TransactionResult | undefined;
  set direct_refund(value: TransactionResult | null | undefined);
  direct_from_cpfp_refund: TransactionResult;
}
export class SecretShareResult {
  free(): void;
  [Symbol.dispose](): void;
  constructor(threshold: number, index: number, share: Uint8Array);
  threshold: number;
  index: number;
  share: Uint8Array;
}
export class SigningCommitment {
  free(): void;
  [Symbol.dispose](): void;
  constructor(hiding: Uint8Array, binding: Uint8Array);
  hiding: Uint8Array;
  binding: Uint8Array;
}
export class SigningNonce {
  free(): void;
  [Symbol.dispose](): void;
  constructor(hiding: Uint8Array, binding: Uint8Array);
  hiding: Uint8Array;
  binding: Uint8Array;
}
export class TimelockResult {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  next_sequence: number;
  next_direct_sequence: number;
}
export class TransactionResult {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  tx: Uint8Array;
  sighash: Uint8Array;
  inputs: TxIn[];
}
/**
 * A stand-in for TxIn.
 */
export class TxIn {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  sequence: number;
}

export type InitInput = RequestInfo | URL | Response | BufferSource | WebAssembly.Module;

export interface InitOutput {
  readonly memory: WebAssembly.Memory;
  readonly __wbg_adaptorsignatureresult_free: (a: number, b: number) => void;
  readonly __wbg_dummytx_free: (a: number, b: number) => void;
  readonly __wbg_get_adaptorsignatureresult_adaptor_private_key: (a: number) => [number, number];
  readonly __wbg_get_adaptorsignatureresult_signature: (a: number) => [number, number];
  readonly __wbg_get_dummytx_txid: (a: number) => [number, number];
  readonly __wbg_get_htlcspendresult_control_block: (a: number) => [number, number];
  readonly __wbg_get_htlcspendresult_script: (a: number) => [number, number];
  readonly __wbg_get_nodetxpairresult_cpfp: (a: number) => number;
  readonly __wbg_get_nodetxpairresult_direct: (a: number) => number;
  readonly __wbg_get_nonceresult_commitment: (a: number) => number;
  readonly __wbg_get_nonceresult_nonce: (a: number) => number;
  readonly __wbg_get_refundtxtrioresult_direct_refund: (a: number) => number;
  readonly __wbg_get_secretshareresult_index: (a: number) => number;
  readonly __wbg_get_secretshareresult_threshold: (a: number) => number;
  readonly __wbg_get_timelockresult_next_direct_sequence: (a: number) => number;
  readonly __wbg_get_timelockresult_next_sequence: (a: number) => number;
  readonly __wbg_get_transactionresult_inputs: (a: number) => [number, number];
  readonly __wbg_htlcspendresult_free: (a: number, b: number) => void;
  readonly __wbg_keypackage_free: (a: number, b: number) => void;
  readonly __wbg_nodetxpairresult_free: (a: number, b: number) => void;
  readonly __wbg_nonceresult_free: (a: number, b: number) => void;
  readonly __wbg_refundtxtrioresult_free: (a: number, b: number) => void;
  readonly __wbg_secretshareresult_free: (a: number, b: number) => void;
  readonly __wbg_set_adaptorsignatureresult_adaptor_private_key: (a: number, b: number, c: number) => void;
  readonly __wbg_set_adaptorsignatureresult_signature: (a: number, b: number, c: number) => void;
  readonly __wbg_set_htlcspendresult_control_block: (a: number, b: number, c: number) => void;
  readonly __wbg_set_htlcspendresult_script: (a: number, b: number, c: number) => void;
  readonly __wbg_set_nodetxpairresult_cpfp: (a: number, b: number) => void;
  readonly __wbg_set_nodetxpairresult_direct: (a: number, b: number) => void;
  readonly __wbg_set_nonceresult_commitment: (a: number, b: number) => void;
  readonly __wbg_set_nonceresult_nonce: (a: number, b: number) => void;
  readonly __wbg_set_refundtxtrioresult_direct_refund: (a: number, b: number) => void;
  readonly __wbg_set_secretshareresult_index: (a: number, b: number) => void;
  readonly __wbg_set_secretshareresult_threshold: (a: number, b: number) => void;
  readonly __wbg_set_timelockresult_next_direct_sequence: (a: number, b: number) => void;
  readonly __wbg_set_timelockresult_next_sequence: (a: number, b: number) => void;
  readonly __wbg_set_transactionresult_inputs: (a: number, b: number, c: number) => void;
  readonly __wbg_signingcommitment_free: (a: number, b: number) => void;
  readonly __wbg_signingnonce_free: (a: number, b: number) => void;
  readonly __wbg_timelockresult_free: (a: number, b: number) => void;
  readonly __wbg_transactionresult_free: (a: number, b: number) => void;
  readonly __wbg_txin_free: (a: number, b: number) => void;
  readonly apply_adaptor_to_signature: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number) => [number, number, number, number];
  readonly check_if_valid_sequence: (a: number) => [number, number];
  readonly compute_multi_input_sighash: (a: number, b: number, c: number, d: any, e: any) => [number, number, number, number];
  readonly construct_direct_refund_tx: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number) => [number, number, number];
  readonly construct_htlc_receiver_spend: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number, i: number, j: number, k: number, l: bigint, m: number, n: number) => [number, number, number];
  readonly construct_htlc_sender_spend: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number, i: number, j: number, k: number, l: bigint, m: number, n: number) => [number, number, number];
  readonly construct_htlc_transaction: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number, i: number, j: number, k: number, l: number, m: bigint, n: number, o: number) => [number, number, number];
  readonly construct_node_tx: (a: number, b: number, c: number, d: number, e: number, f: number) => [number, number, number];
  readonly construct_node_tx_pair: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: bigint) => [number, number, number];
  readonly construct_refund_tx: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number) => [number, number, number];
  readonly construct_refund_tx_trio: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number, i: number, j: number, k: number, l: bigint) => [number, number, number];
  readonly construct_split_tx: (a: number, b: number, c: number, d: number, e: number, f: number) => [number, number, number];
  readonly create_dummy_tx: (a: number, b: number, c: bigint) => [number, number, number];
  readonly decrypt_ecies: (a: number, b: number, c: number, d: number) => [number, number, number, number];
  readonly encrypt_ecies: (a: number, b: number, c: number, d: number) => [number, number, number, number];
  readonly ffi_spark_frost_rust_future_cancel_f32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_f64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_i16: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_i32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_i64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_i8: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_pointer: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_rust_buffer: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_u16: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_u32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_u64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_u8: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_cancel_void: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_complete_f32: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_f64: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_i16: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_i32: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_i64: (a: bigint, b: number) => bigint;
  readonly ffi_spark_frost_rust_future_complete_i8: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_pointer: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_rust_buffer: (a: number, b: bigint, c: number) => void;
  readonly ffi_spark_frost_rust_future_complete_u16: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_u32: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_u64: (a: bigint, b: number) => bigint;
  readonly ffi_spark_frost_rust_future_complete_u8: (a: bigint, b: number) => number;
  readonly ffi_spark_frost_rust_future_complete_void: (a: bigint, b: number) => void;
  readonly ffi_spark_frost_rust_future_free_f32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_f64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_i16: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_i32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_i64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_i8: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_pointer: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_rust_buffer: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_u16: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_u32: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_u64: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_u8: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_free_void: (a: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_f32: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_f64: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_i16: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_i32: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_i64: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_i8: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_pointer: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_rust_buffer: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_u16: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_u32: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_u64: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_u8: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rust_future_poll_void: (a: bigint, b: number, c: bigint) => void;
  readonly ffi_spark_frost_rustbuffer_alloc: (a: number, b: bigint, c: number) => void;
  readonly ffi_spark_frost_rustbuffer_free: (a: number, b: number) => void;
  readonly ffi_spark_frost_rustbuffer_from_bytes: (a: number, b: number, c: number) => void;
  readonly ffi_spark_frost_rustbuffer_reserve: (a: number, b: number, c: bigint, d: number) => void;
  readonly ffi_spark_frost_uniffi_contract_version: () => number;
  readonly frost_nonce: (a: number) => [number, number, number];
  readonly generate_adaptor_from_signature: (a: number, b: number) => [number, number, number];
  readonly generate_signature_from_existing_adaptor: (a: number, b: number, c: number, d: number) => [number, number, number, number];
  readonly get_public_key_bytes: (a: number, b: number, c: number) => [number, number, number, number];
  readonly get_taproot_pubkey: (a: number, b: number) => [number, number, number, number];
  readonly get_timelock_from_sequence: (a: number) => number;
  readonly is_zero_timelock: (a: number) => number;
  readonly keypackage_new: (a: number, b: number, c: number, d: number, e: number, f: number) => number;
  readonly next_sequence: (a: number, b: number, c: number) => [number, number, number];
  readonly random_secret_key_bytes: () => [number, number, number, number];
  readonly recover_secret_wasm: (a: any) => [number, number, number, number];
  readonly secretshareresult_new: (a: number, b: number, c: number, d: number) => number;
  readonly signingcommitment_new: (a: number, b: number, c: number, d: number) => number;
  readonly split_secret: (a: number, b: number, c: number, d: number) => [number, number, number, number];
  readonly split_secret_with_proofs: (a: number, b: number, c: number, d: number) => [number, number, number];
  readonly uniffi_spark_frost_checksum_func_aggregate_frost: () => number;
  readonly uniffi_spark_frost_checksum_func_apply_adaptor_to_signature: () => number;
  readonly uniffi_spark_frost_checksum_func_check_if_valid_sequence: () => number;
  readonly uniffi_spark_frost_checksum_func_compute_multi_input_sighash_uniffi: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_direct_refund_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_htlc_receiver_spend: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_htlc_sender_spend: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_htlc_transaction: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_node_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_node_tx_pair: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_refund_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_refund_tx_trio: () => number;
  readonly uniffi_spark_frost_checksum_func_construct_split_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_create_dummy_tx: () => number;
  readonly uniffi_spark_frost_checksum_func_decrypt_ecies: () => number;
  readonly uniffi_spark_frost_checksum_func_encrypt_ecies: () => number;
  readonly uniffi_spark_frost_checksum_func_frost_nonce: () => number;
  readonly uniffi_spark_frost_checksum_func_generate_adaptor_from_signature: () => number;
  readonly uniffi_spark_frost_checksum_func_generate_signature_from_existing_adaptor: () => number;
  readonly uniffi_spark_frost_checksum_func_get_public_key_bytes: () => number;
  readonly uniffi_spark_frost_checksum_func_get_taproot_pubkey: () => number;
  readonly uniffi_spark_frost_checksum_func_get_timelock_from_sequence: () => number;
  readonly uniffi_spark_frost_checksum_func_is_zero_timelock: () => number;
  readonly uniffi_spark_frost_checksum_func_next_sequence: () => number;
  readonly uniffi_spark_frost_checksum_func_random_secret_key_bytes: () => number;
  readonly uniffi_spark_frost_checksum_func_recover_secret_uniffi: () => number;
  readonly uniffi_spark_frost_checksum_func_round_down_to_timelock_interval: () => number;
  readonly uniffi_spark_frost_checksum_func_sign_frost: () => number;
  readonly uniffi_spark_frost_checksum_func_split_secret: () => number;
  readonly uniffi_spark_frost_checksum_func_split_secret_with_proofs_uniffi: () => number;
  readonly uniffi_spark_frost_checksum_func_validate_adaptor_signature: () => number;
  readonly uniffi_spark_frost_checksum_func_validate_share_uniffi: () => number;
  readonly uniffi_spark_frost_checksum_func_validate_signature_share: () => number;
  readonly uniffi_spark_frost_checksum_func_verify_signature_bytes: () => number;
  readonly uniffi_spark_frost_fn_func_aggregate_frost: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number, i: number, j: number, k: number) => void;
  readonly uniffi_spark_frost_fn_func_apply_adaptor_to_signature: (a: number, b: number, c: number, d: number, e: number, f: number) => void;
  readonly uniffi_spark_frost_fn_func_check_if_valid_sequence: (a: number, b: number) => void;
  readonly uniffi_spark_frost_fn_func_compute_multi_input_sighash_uniffi: (a: number, b: number, c: number, d: number, e: number, f: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_direct_refund_tx: (a: number, b: number, c: number, d: number, e: number, f: number, g: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_htlc_receiver_spend: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: bigint, i: number, j: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_htlc_sender_spend: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: bigint, i: number, j: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_htlc_transaction: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number, i: number, j: bigint, k: number, l: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_node_tx: (a: number, b: number, c: number, d: number, e: number, f: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_node_tx_pair: (a: number, b: number, c: number, d: number, e: number, f: number, g: bigint, h: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_refund_tx: (a: number, b: number, c: number, d: number, e: number, f: number, g: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_refund_tx_trio: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number, i: bigint, j: number) => void;
  readonly uniffi_spark_frost_fn_func_construct_split_tx: (a: number, b: number, c: number, d: number, e: number, f: number) => void;
  readonly uniffi_spark_frost_fn_func_create_dummy_tx: (a: number, b: number, c: bigint, d: number) => void;
  readonly uniffi_spark_frost_fn_func_decrypt_ecies: (a: number, b: number, c: number, d: number) => void;
  readonly uniffi_spark_frost_fn_func_encrypt_ecies: (a: number, b: number, c: number, d: number) => void;
  readonly uniffi_spark_frost_fn_func_frost_nonce: (a: number, b: number, c: number) => void;
  readonly uniffi_spark_frost_fn_func_generate_adaptor_from_signature: (a: number, b: number, c: number) => void;
  readonly uniffi_spark_frost_fn_func_generate_signature_from_existing_adaptor: (a: number, b: number, c: number, d: number) => void;
  readonly uniffi_spark_frost_fn_func_get_public_key_bytes: (a: number, b: number, c: number, d: number) => void;
  readonly uniffi_spark_frost_fn_func_get_taproot_pubkey: (a: number, b: number, c: number) => void;
  readonly uniffi_spark_frost_fn_func_get_timelock_from_sequence: (a: number, b: number) => number;
  readonly uniffi_spark_frost_fn_func_is_zero_timelock: (a: number, b: number) => number;
  readonly uniffi_spark_frost_fn_func_next_sequence: (a: number, b: number, c: number, d: number, e: number) => void;
  readonly uniffi_spark_frost_fn_func_random_secret_key_bytes: (a: number, b: number) => void;
  readonly uniffi_spark_frost_fn_func_recover_secret_uniffi: (a: number, b: number, c: number) => void;
  readonly uniffi_spark_frost_fn_func_round_down_to_timelock_interval: (a: number, b: number, c: number) => number;
  readonly uniffi_spark_frost_fn_func_sign_frost: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number) => void;
  readonly uniffi_spark_frost_fn_func_split_secret: (a: number, b: number, c: number, d: number, e: number) => void;
  readonly uniffi_spark_frost_fn_func_split_secret_with_proofs_uniffi: (a: number, b: number, c: number, d: number, e: number) => void;
  readonly uniffi_spark_frost_fn_func_validate_adaptor_signature: (a: number, b: number, c: number, d: number, e: number) => void;
  readonly uniffi_spark_frost_fn_func_validate_share_uniffi: (a: number, b: number, c: number, d: number, e: number) => void;
  readonly uniffi_spark_frost_fn_func_validate_signature_share: (a: number, b: number, c: number, d: number, e: number, f: number, g: number) => number;
  readonly uniffi_spark_frost_fn_func_verify_signature_bytes: (a: number, b: number, c: number, d: number) => number;
  readonly validate_adaptor_signature: (a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number) => [number, number];
  readonly validate_share: (a: number, b: number, c: number, d: number, e: any) => [number, number];
  readonly verify_signature_bytes: (a: number, b: number, c: number, d: number, e: number, f: number) => [number, number, number];
  readonly wasm_aggregate_frost: (a: number, b: number, c: any, d: number, e: any, f: number, g: number, h: any, i: number, j: number, k: number, l: number, m: number, n: number) => [number, number, number, number];
  readonly wasm_sign_frost: (a: number, b: number, c: number, d: number, e: number, f: any, g: number, h: number) => [number, number, number, number];
  readonly signingnonce_new: (a: number, b: number, c: number, d: number) => number;
  readonly __wbg_set_refundtxtrioresult_cpfp_refund: (a: number, b: number) => void;
  readonly __wbg_set_refundtxtrioresult_direct_from_cpfp_refund: (a: number, b: number) => void;
  readonly __wbg_set_txin_sequence: (a: number, b: number) => void;
  readonly __wbg_set_dummytx_tx: (a: number, b: number, c: number) => void;
  readonly __wbg_set_dummytx_txid: (a: number, b: number, c: number) => void;
  readonly __wbg_set_htlcspendresult_sighash: (a: number, b: number, c: number) => void;
  readonly __wbg_set_htlcspendresult_tx: (a: number, b: number, c: number) => void;
  readonly __wbg_set_keypackage_public_key: (a: number, b: number, c: number) => void;
  readonly __wbg_set_keypackage_secret_key: (a: number, b: number, c: number) => void;
  readonly __wbg_set_keypackage_verifying_key: (a: number, b: number, c: number) => void;
  readonly __wbg_set_secretshareresult_share: (a: number, b: number, c: number) => void;
  readonly __wbg_set_signingcommitment_binding: (a: number, b: number, c: number) => void;
  readonly __wbg_set_signingcommitment_hiding: (a: number, b: number, c: number) => void;
  readonly __wbg_set_signingnonce_binding: (a: number, b: number, c: number) => void;
  readonly __wbg_set_signingnonce_hiding: (a: number, b: number, c: number) => void;
  readonly __wbg_set_transactionresult_sighash: (a: number, b: number, c: number) => void;
  readonly __wbg_set_transactionresult_tx: (a: number, b: number, c: number) => void;
  readonly round_down_to_timelock_interval: (a: number, b: number) => number;
  readonly __wbg_get_refundtxtrioresult_cpfp_refund: (a: number) => number;
  readonly __wbg_get_refundtxtrioresult_direct_from_cpfp_refund: (a: number) => number;
  readonly __wbg_get_dummytx_tx: (a: number) => [number, number];
  readonly __wbg_get_htlcspendresult_sighash: (a: number) => [number, number];
  readonly __wbg_get_htlcspendresult_tx: (a: number) => [number, number];
  readonly __wbg_get_keypackage_public_key: (a: number) => [number, number];
  readonly __wbg_get_keypackage_secret_key: (a: number) => [number, number];
  readonly __wbg_get_keypackage_verifying_key: (a: number) => [number, number];
  readonly __wbg_get_secretshareresult_share: (a: number) => [number, number];
  readonly __wbg_get_signingcommitment_binding: (a: number) => [number, number];
  readonly __wbg_get_signingcommitment_hiding: (a: number) => [number, number];
  readonly __wbg_get_signingnonce_binding: (a: number) => [number, number];
  readonly __wbg_get_signingnonce_hiding: (a: number) => [number, number];
  readonly __wbg_get_transactionresult_sighash: (a: number) => [number, number];
  readonly __wbg_get_transactionresult_tx: (a: number) => [number, number];
  readonly __wbg_get_txin_sequence: (a: number) => number;
  readonly rustsecp256k1_v0_10_0_context_create: (a: number) => number;
  readonly rustsecp256k1_v0_10_0_context_destroy: (a: number) => void;
  readonly rustsecp256k1_v0_10_0_default_error_callback_fn: (a: number, b: number) => void;
  readonly rustsecp256k1_v0_10_0_default_illegal_callback_fn: (a: number, b: number) => void;
  readonly __wbindgen_malloc: (a: number, b: number) => number;
  readonly __wbindgen_realloc: (a: number, b: number, c: number, d: number) => number;
  readonly __wbindgen_exn_store: (a: number) => void;
  readonly __externref_table_alloc: () => number;
  readonly __wbindgen_externrefs: WebAssembly.Table;
  readonly __externref_table_dealloc: (a: number) => void;
  readonly __wbindgen_free: (a: number, b: number, c: number) => void;
  readonly __externref_drop_slice: (a: number, b: number) => void;
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
