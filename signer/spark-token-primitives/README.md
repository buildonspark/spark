# spark-token-primitives

Reusable Rust client helpers for constructing Spark token transfer transactions
locally.

This crate owns the actual Rust transaction-building, hashing, and protobuf
assembly logic. Language-specific bindings should live in adapter crates such as
`spark-token-primitives-uniffi`.
