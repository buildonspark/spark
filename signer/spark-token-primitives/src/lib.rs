pub mod proto;
mod token_transaction;

#[derive(Debug, Clone, thiserror::Error)]
pub enum SparkTokenPrimitivesError {
    #[error("Spark token primitives error: {0}")]
    Spark(String),
}

impl From<String> for SparkTokenPrimitivesError {
    fn from(value: String) -> Self {
        Self::Spark(value)
    }
}

impl From<&str> for SparkTokenPrimitivesError {
    fn from(value: &str) -> Self {
        Self::Spark(value.to_owned())
    }
}

#[derive(Debug, Clone)]
pub struct SelectedTokenOutput {
    pub previous_transaction_hash: Vec<u8>,
    pub previous_transaction_vout: u32,
    pub owner_public_key: Vec<u8>,
    pub token_identifier: Vec<u8>,
    pub token_amount: Vec<u8>,
}

#[derive(Debug, Clone)]
pub struct ReceiverTokenOutput {
    pub receiver_spark_address: String,
    pub token_identifier: Option<Vec<u8>>,
    pub token_amount: Option<Vec<u8>>,
}

#[derive(Debug, Clone)]
pub struct TransferBuildRequest {
    pub identity_public_key: Vec<u8>,
    pub selected_outputs: Vec<SelectedTokenOutput>,
    pub receiver_outputs: Vec<ReceiverTokenOutput>,
    pub operator_identity_public_keys: Vec<Vec<u8>>,
    pub network: u32,
    pub validity_duration_seconds: u64,
    pub client_created_timestamp_unix_micros: i64,
    pub withdraw_bond_sats: u64,
    pub withdraw_relative_block_locktime: u64,
}

#[derive(Debug, Clone)]
pub struct PartialTransferBuildResult {
    pub partial_token_transaction_bytes: Vec<u8>,
    pub partial_token_transaction_hash: Vec<u8>,
}

#[derive(Debug, Clone)]
pub struct SignatureWithIndexInput {
    pub input_index: u32,
    pub public_key: Vec<u8>,
    pub signature: Vec<u8>,
}

#[derive(Debug, Clone)]
pub struct BroadcastBuildRequest {
    pub identity_public_key: Vec<u8>,
    pub partial_token_transaction_bytes: Vec<u8>,
    pub owner_signatures: Vec<SignatureWithIndexInput>,
}

pub fn construct_partial_transfer_transaction(
    request: TransferBuildRequest,
) -> Result<PartialTransferBuildResult, SparkTokenPrimitivesError> {
    token_transaction::construct_partial_transfer_transaction_impl(request)
}

pub fn hash_partial_token_transaction(
    partial_token_transaction_bytes: Vec<u8>,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    token_transaction::hash_partial_token_transaction_impl(&partial_token_transaction_bytes)
}

pub fn build_broadcast_transaction_request(
    request: BroadcastBuildRequest,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    token_transaction::build_broadcast_transaction_request_impl(request)
}
