# spark-token-primitives-uniffi

UniFFI bindings for the reusable `spark-token-primitives` Rust crate.

## Scope

This crate keeps the Python-facing UniFFI surface.

The actual Rust token-transaction construction, hashing, and protobuf assembly
logic lives in `signer/spark-token-primitives/` so it can be reused by other Rust
consumers.

## Generate Python bindings

```sh
cd signer/spark-token-primitives-uniffi
cargo run --bin uniffi-bindgen generate src/spark_token_primitives.udl --language python --out-dir spark-token-primitives-python/src/spark_token_primitives/ --no-format
cargo build --profile release-smaller
```
