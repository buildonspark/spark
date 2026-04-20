# spark-token-primitives

Client-side Rust library for constructing [Spark](https://www.spark.info) token transfer transactions and invoices locally, without network access.

This crate owns the transaction-building, hashing, and protobuf assembly logic for Spark token operations.

## Overview

Spark tokens are Bitcoin-native assets that transfer off-chain via the Spark Layer 2 protocol. This crate provides the building blocks needed to construct and sign token transactions client-side before submitting them to a Spark Operator cluster.

The typical flow for a token transfer is:

1. **Construct** a partial transaction from your selected inputs and intended outputs — `construct_partial_transfer_transaction` (returns the transaction bytes and its hash)
2. Sign the hash with each input's owner key (outside this crate)
3. **Broadcast** by assembling the final transaction with your signatures — `build_broadcast_transaction_request`

For invoice-based transfers, where the receiver generates a Spark address embedding payment details:

1. **Prepare** an invoice, producing a Bech32m-encoded unsigned Spark address and a hash to sign — `prepare_token_invoice`
2. Sign the hash (outside this crate)
3. **Finalize** the invoice by attaching the signature to produce the shareable Spark address — `finalize_token_invoice`

## API

### Token transfers

```rust
use spark_token_primitives::{
    construct_partial_transfer_transaction,
    build_broadcast_transaction_request, TransferBuildRequest, SelectedTokenOutput,
    ReceiverTokenOutput, BroadcastBuildRequest, SignatureWithIndexInput,
};

// Step 1: construct the partial transaction
let result = construct_partial_transfer_transaction(TransferBuildRequest {
    identity_public_key: sender_pubkey,          // 33-byte compressed public key
    selected_outputs: vec![SelectedTokenOutput {
        previous_transaction_hash: prev_hash,    // 32 bytes
        previous_transaction_vout: 0,
        owner_public_key: owner_pubkey,          // 33 bytes
        token_identifier: token_id,              // 32 bytes
        token_amount: amount_bytes,              // 16-byte big-endian u128
    }],
    receiver_outputs: vec![ReceiverTokenOutput {
        receiver_spark_address: "spark1...".to_string(),
        token_identifier: Some(token_id),
        token_amount: Some(amount_bytes),
    }],
    operator_identity_public_keys: operator_pubkeys,
    network: 1, // Network::Mainnet
    validity_duration_seconds: 60,
    client_created_timestamp_unix_micros: timestamp,
    withdraw_bond_sats: 1000,
    withdraw_relative_block_locktime: 144,
})?;

// Step 2: sign the pre-computed hash from the result, then broadcast
// (result.partial_token_transaction_hash is already computed by Step 1)
let broadcast_bytes = build_broadcast_transaction_request(BroadcastBuildRequest {
    identity_public_key: sender_pubkey,
    partial_token_transaction_bytes: result.partial_token_transaction_bytes,
    owner_signatures: vec![SignatureWithIndexInput {
        input_index: 0,
        public_key: owner_pubkey,
        signature: my_signature, // 64-byte Schnorr signature over result.partial_token_transaction_hash
    }],
})?;
```

### Token invoices

```rust
use spark_token_primitives::{
    prepare_token_invoice, finalize_token_invoice,
    PrepareTokenInvoiceRequest, FinalizeTokenInvoiceRequest,
};

// Receiver side: prepare an invoice
let invoice = prepare_token_invoice(PrepareTokenInvoiceRequest {
    receiver_identity_public_key: receiver_pubkey, // 33 bytes
    network: 1,
    token_identifier: Some(token_id),
    token_amount: Some(amount_bytes),
    memo: Some("payment for goods".to_string()),
    sender_spark_address: None,
    expiry_time_unix_millis: None,
    invoice_id: None, // auto-generated if None
})?;
// Sign invoice.spark_invoice_hash externally, then finalize:
let spark_address = finalize_token_invoice(FinalizeTokenInvoiceRequest {
    receiver_identity_public_key: receiver_pubkey,
    network: 1,
    spark_invoice_fields_bytes: invoice.spark_invoice_fields_bytes,
    signature: Some(my_signature),
})?;
// Share `spark_address` with the sender
```

## Encoding conventions

- **Token amounts**: 16-byte big-endian `u128` for transfer inputs/outputs; invoice amounts accept 1–16 bytes (zero-padded to 16 on the left when resolved)
- **Token identifiers**: 32-byte opaque identifiers
- **Public keys**: 33-byte compressed secp256k1 keys
- **Spark addresses**: Bech32m-encoded protobuf payloads with network-specific HRPs (`spark`, `sparkt`, `sparks`, `sparkrt`)

## License

Apache-2.0
