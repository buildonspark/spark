//! Hashing for token transaction protos.
//!
//! Both partial and final hashes are now produced by the generic
//! [`crate::protohash`] module, which mirrors `protohash.Hash` in
//! `spark/common/protohash/hasher.go` (Go) and `createProtoHasher` in
//! `sdks/js/packages/spark-sdk/src/spark-wallet/proto-hash.ts` (JS).
//!
//! Cross-language fixtures in
//! `spark/testdata/token_transaction_v3_hash_cases.json` verify that
//! all three implementations agree.

use prost::Message;

use crate::proto::spark_token::{FinalTokenTransaction, PartialTokenTransaction};
use crate::protohash::hash_proto;
use crate::SparkTokenPrimitivesError;

pub(crate) fn hash_partial_token_transaction_impl(
    partial_token_transaction_bytes: &[u8],
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let msg = PartialTokenTransaction::decode(partial_token_transaction_bytes).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("failed to decode PartialTokenTransaction: {err}"))
    })?;
    hash_partial_token_transaction(&msg)
}

pub(crate) fn hash_final_token_transaction_impl(
    final_token_transaction_bytes: &[u8],
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let msg = FinalTokenTransaction::decode(final_token_transaction_bytes).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("failed to decode FinalTokenTransaction: {err}"))
    })?;
    hash_final_token_transaction(&msg)
}

pub(super) fn hash_partial_token_transaction(
    msg: &PartialTokenTransaction,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    hash_proto(msg)
        .map(|h| h.to_vec())
        .map_err(|err| SparkTokenPrimitivesError::Spark(err.to_string()))
}

pub(super) fn hash_final_token_transaction(
    msg: &FinalTokenTransaction,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    hash_proto(msg)
        .map(|h| h.to_vec())
        .map_err(|err| SparkTokenPrimitivesError::Spark(err.to_string()))
}

pub(super) fn hex_string(bytes: &[u8]) -> String {
    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        use std::fmt::Write as _;
        let _ = write!(output, "{byte:02x}");
    }
    output
}
