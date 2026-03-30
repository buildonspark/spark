// Uniffi generates code with bad comments for some reason...
#![allow(clippy::empty_line_after_doc_comments)]
uniffi::include_scaffolding!("spark_token_primitives");

pub use spark_token_primitives::{
    BroadcastBuildRequest, PartialTransferBuildResult, ReceiverTokenOutput, SelectedTokenOutput,
    SignatureWithIndexInput, SparkTokenPrimitivesError, TransferBuildRequest,
};

pub fn construct_partial_transfer_transaction(
    request: TransferBuildRequest,
) -> Result<PartialTransferBuildResult, SparkTokenPrimitivesError> {
    spark_token_primitives::construct_partial_transfer_transaction(request)
}

pub fn hash_partial_token_transaction(
    partial_token_transaction_bytes: Vec<u8>,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    spark_token_primitives::hash_partial_token_transaction(partial_token_transaction_bytes)
}

pub fn build_broadcast_transaction_request(
    request: BroadcastBuildRequest,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    spark_token_primitives::build_broadcast_transaction_request(request)
}
