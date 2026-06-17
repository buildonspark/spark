//! Generic protobuf hashing, mirroring `spark/common/protohash/hasher.go`
//! (Go) and `sdks/js/packages/spark-sdk/src/spark-wallet/proto-hash.ts` (JS).
//!
//! Walks any `prost_reflect::DynamicMessage` by field descriptor and produces
//! a deterministic SHA-256 hash that matches the Go/JS implementations
//! bit-for-bit on the cross-language fixtures in
//! `spark/testdata/token_transaction_v3_hash_cases.json`.

mod errors;
mod google_protobuf;
mod hashing;

use prost_reflect::{DescriptorPool, ReflectMessage};
use std::sync::LazyLock;

pub use errors::ProtoHasherError;
pub use hashing::hash_message;

/// Descriptor pool generated at build time by `prost-reflect-build`.
pub static DESCRIPTOR_POOL: LazyLock<DescriptorPool> = LazyLock::new(|| {
    DescriptorPool::decode(
        include_bytes!(concat!(env!("OUT_DIR"), "/file_descriptor_set.bin")).as_ref(),
    )
    .expect("failed to decode bundled file_descriptor_set.bin")
});

/// Returns the hash of the given message, or an error if it cannot be hashed.
///
/// Top-level scalar/value wrapper types (`google.protobuf.BoolValue`,
/// `google.protobuf.Value`, etc.) are rejected — wrap them in a parent
/// message field instead. This matches the Go implementation.
pub fn hash_proto<M: prost_reflect::ReflectMessage>(
    message: &M,
) -> Result<[u8; 32], ProtoHasherError> {
    let dynamic = message.transcode_to_dynamic();
    let descriptor = dynamic.descriptor();
    match descriptor.full_name() {
        "google.protobuf.Value"
        | "google.protobuf.ListValue"
        | "google.protobuf.BoolValue"
        | "google.protobuf.Int32Value"
        | "google.protobuf.Int64Value"
        | "google.protobuf.UInt32Value"
        | "google.protobuf.UInt64Value"
        | "google.protobuf.FloatValue"
        | "google.protobuf.DoubleValue"
        | "google.protobuf.StringValue"
        | "google.protobuf.BytesValue" => Err(ProtoHasherError::TopLevelScalarNotHashable(
            descriptor.full_name().to_owned(),
        )),
        _ => hash_message(dynamic)?.ok_or(ProtoHasherError::NothingToHash),
    }
}
