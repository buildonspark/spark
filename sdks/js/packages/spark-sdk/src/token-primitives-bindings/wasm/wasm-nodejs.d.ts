/* tslint:disable */
/* eslint-disable */

export function build_broadcast_transaction_request(request: any): Uint8Array;

export function construct_partial_transfer_transaction(request: any): any;

export function coop_exit_target(withdrawal_address: string, leaf_set_hash: Uint8Array): Uint8Array;

export function finalize_token_invoice(request: any): string;

export function hash_partial_token_transaction(partial_token_transaction_bytes: Uint8Array): Uint8Array;

export function hash_transfer_manifest(transfer_manifest_bytes: Uint8Array): Uint8Array;

export function prepare_token_invoice(request: any): any;

export function quote_envelope_digest(network: number, manifest_hash: Uint8Array, reason: number, role: number, target: Uint8Array): Uint8Array;

export function receive_attestor_target(payment_hash: Uint8Array): Uint8Array;

export function send_target(payment_hash: Uint8Array): Uint8Array;

export function static_deposit_target(txid: Uint8Array, vout: number): Uint8Array;
