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
