# @buildonspark/spark-frost-bare-addon

## 0.0.5

### Patch Changes

- - **(Rust bindings)**
    - `TransactionResult` now exposes:
      - `tx: Uint8Array`
      - `sighash: Uint8Array`
      - `inputs: TxIn[]` where `TxIn` includes a `sequence` field.
    - Allows you to inspect per‑input sequences/timelocks when calling helpers like `construct_node_tx`, `construct_refund_tx`, `construct_split_tx`, and `construct_direct_refund_tx`.

## 0.0.4

### Patch Changes

- - Update deps

## 0.0.3

### Patch Changes

- - Remove debug log from binding.rs

## 0.0.2

### Patch Changes

- - Fix export of decryptEcies

## 0.0.1

### Patch Changes

- Initial publish
