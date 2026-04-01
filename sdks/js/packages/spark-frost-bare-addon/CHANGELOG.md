# @buildonspark/spark-frost-bare-addon

## 0.0.10

### Patch Changes

- Add constructNodeTxPair, constructRefundTxTrio, computeMultiInputSighash

## 0.0.9

### Patch Changes

- - Regenerate all prebuilds

## 0.0.8

### Patch Changes

- - Add native VSS (Verifiable Secret Sharing) functions to the bare addon:
    - splitSecretWithProofs(secret, threshold, numShares) - Split a secret into verifiable shares with commitment proofs
    - recoverSecret(shares) - Recover a secret from a threshold number of shares
    - validateShare(share, index, threshold, proofs) - Validate a share against its commitment proofs

## 0.0.7

### Patch Changes

- - Upgrade `@noble/curves` minimum version to `^1.9.7`

## 0.0.6

### Patch Changes

- ### Updated Rust Version Requirement

  Updated minimum Rust version to 1.92.0 to be compatible with new spark-frost-python

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
