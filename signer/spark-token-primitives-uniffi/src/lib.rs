// Uniffi generates code with bad comments for some reason...
#![allow(clippy::empty_line_after_doc_comments)]
uniffi::include_scaffolding!("spark_token_primitives");

#[cfg(target_arch = "wasm32")]
use serde::{Deserialize, Serialize};
#[cfg(target_arch = "wasm32")]
use serde_bytes::ByteBuf;
#[cfg(target_arch = "wasm32")]
use wasm_bindgen::prelude::*;

pub use spark_token_primitives::{
    BroadcastBuildRequest, FinalizeTokenInvoiceRequest, PartialTransferBuildResult,
    PrepareTokenInvoiceRequest, PreparedTokenInvoice, ReceiverTokenOutput, SelectedTokenOutput,
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

pub fn prepare_token_invoice(
    request: PrepareTokenInvoiceRequest,
) -> Result<PreparedTokenInvoice, SparkTokenPrimitivesError> {
    spark_token_primitives::prepare_token_invoice(request)
}

pub fn finalize_token_invoice(
    request: FinalizeTokenInvoiceRequest,
) -> Result<String, SparkTokenPrimitivesError> {
    spark_token_primitives::finalize_token_invoice(request)
}

#[cfg(target_arch = "wasm32")]
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WasmSelectedTokenOutput {
    previous_transaction_hash: ByteBuf,
    previous_transaction_vout: u32,
    owner_public_key: ByteBuf,
    token_identifier: ByteBuf,
    token_amount: ByteBuf,
}

#[cfg(target_arch = "wasm32")]
impl From<WasmSelectedTokenOutput> for SelectedTokenOutput {
    fn from(value: WasmSelectedTokenOutput) -> Self {
        Self {
            previous_transaction_hash: value.previous_transaction_hash.into_vec(),
            previous_transaction_vout: value.previous_transaction_vout,
            owner_public_key: value.owner_public_key.into_vec(),
            token_identifier: value.token_identifier.into_vec(),
            token_amount: value.token_amount.into_vec(),
        }
    }
}

#[cfg(target_arch = "wasm32")]
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WasmReceiverTokenOutput {
    receiver_spark_address: String,
    token_identifier: Option<ByteBuf>,
    token_amount: Option<ByteBuf>,
}

#[cfg(target_arch = "wasm32")]
impl From<WasmReceiverTokenOutput> for ReceiverTokenOutput {
    fn from(value: WasmReceiverTokenOutput) -> Self {
        Self {
            receiver_spark_address: value.receiver_spark_address,
            token_identifier: value.token_identifier.map(ByteBuf::into_vec),
            token_amount: value.token_amount.map(ByteBuf::into_vec),
        }
    }
}

#[cfg(target_arch = "wasm32")]
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WasmTransferBuildRequest {
    identity_public_key: ByteBuf,
    selected_outputs: Vec<WasmSelectedTokenOutput>,
    receiver_outputs: Vec<WasmReceiverTokenOutput>,
    operator_identity_public_keys: Vec<ByteBuf>,
    network: u32,
    validity_duration_seconds: u64,
    client_created_timestamp_unix_micros: i64,
    withdraw_bond_sats: u64,
    withdraw_relative_block_locktime: u64,
    execute_before_unix_micros: Option<i64>,
}

#[cfg(target_arch = "wasm32")]
impl From<WasmTransferBuildRequest> for TransferBuildRequest {
    fn from(value: WasmTransferBuildRequest) -> Self {
        Self {
            identity_public_key: value.identity_public_key.into_vec(),
            selected_outputs: value.selected_outputs.into_iter().map(Into::into).collect(),
            receiver_outputs: value.receiver_outputs.into_iter().map(Into::into).collect(),
            operator_identity_public_keys: value
                .operator_identity_public_keys
                .into_iter()
                .map(ByteBuf::into_vec)
                .collect(),
            network: value.network,
            validity_duration_seconds: value.validity_duration_seconds,
            client_created_timestamp_unix_micros: value.client_created_timestamp_unix_micros,
            withdraw_bond_sats: value.withdraw_bond_sats,
            withdraw_relative_block_locktime: value.withdraw_relative_block_locktime,
            execute_before_unix_micros: value.execute_before_unix_micros,
        }
    }
}

#[cfg(target_arch = "wasm32")]
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WasmSignatureWithIndexInput {
    input_index: u32,
    public_key: ByteBuf,
    signature: ByteBuf,
}

#[cfg(target_arch = "wasm32")]
impl From<WasmSignatureWithIndexInput> for SignatureWithIndexInput {
    fn from(value: WasmSignatureWithIndexInput) -> Self {
        Self {
            input_index: value.input_index,
            public_key: value.public_key.into_vec(),
            signature: value.signature.into_vec(),
        }
    }
}

#[cfg(target_arch = "wasm32")]
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WasmBroadcastBuildRequest {
    identity_public_key: ByteBuf,
    partial_token_transaction_bytes: ByteBuf,
    owner_signatures: Vec<WasmSignatureWithIndexInput>,
}

#[cfg(target_arch = "wasm32")]
impl From<WasmBroadcastBuildRequest> for BroadcastBuildRequest {
    fn from(value: WasmBroadcastBuildRequest) -> Self {
        Self {
            identity_public_key: value.identity_public_key.into_vec(),
            partial_token_transaction_bytes: value.partial_token_transaction_bytes.into_vec(),
            owner_signatures: value.owner_signatures.into_iter().map(Into::into).collect(),
        }
    }
}

#[cfg(target_arch = "wasm32")]
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WasmPrepareTokenInvoiceRequest {
    receiver_identity_public_key: ByteBuf,
    network: u32,
    token_identifier: Option<ByteBuf>,
    token_amount: Option<ByteBuf>,
    memo: Option<String>,
    sender_spark_address: Option<String>,
    expiry_time_unix_millis: Option<u64>,
    invoice_id: Option<ByteBuf>,
}

#[cfg(target_arch = "wasm32")]
impl From<WasmPrepareTokenInvoiceRequest> for PrepareTokenInvoiceRequest {
    fn from(value: WasmPrepareTokenInvoiceRequest) -> Self {
        Self {
            receiver_identity_public_key: value.receiver_identity_public_key.into_vec(),
            network: value.network,
            token_identifier: value.token_identifier.map(ByteBuf::into_vec),
            token_amount: value.token_amount.map(ByteBuf::into_vec),
            memo: value.memo,
            sender_spark_address: value.sender_spark_address,
            expiry_time_unix_millis: value.expiry_time_unix_millis,
            invoice_id: value.invoice_id.map(ByteBuf::into_vec),
        }
    }
}

#[cfg(target_arch = "wasm32")]
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WasmFinalizeTokenInvoiceRequest {
    receiver_identity_public_key: ByteBuf,
    network: u32,
    spark_invoice_fields_bytes: ByteBuf,
    signature: Option<ByteBuf>,
}

#[cfg(target_arch = "wasm32")]
impl From<WasmFinalizeTokenInvoiceRequest> for FinalizeTokenInvoiceRequest {
    fn from(value: WasmFinalizeTokenInvoiceRequest) -> Self {
        Self {
            receiver_identity_public_key: value.receiver_identity_public_key.into_vec(),
            network: value.network,
            spark_invoice_fields_bytes: value.spark_invoice_fields_bytes.into_vec(),
            signature: value.signature.map(ByteBuf::into_vec),
        }
    }
}

#[cfg(target_arch = "wasm32")]
fn js_error<E: std::fmt::Display>(err: E) -> JsValue {
    JsValue::from_str(&err.to_string())
}

#[cfg(target_arch = "wasm32")]
fn set_js_property(object: &js_sys::Object, name: &str, value: &JsValue) -> Result<(), JsValue> {
    js_sys::Reflect::set(object, &JsValue::from_str(name), value).map(|_| ())
}

#[cfg(target_arch = "wasm32")]
fn js_bytes(bytes: &[u8]) -> JsValue {
    js_sys::Uint8Array::from(bytes).into()
}

#[cfg(target_arch = "wasm32")]
fn partial_transfer_build_result_to_js(
    result: PartialTransferBuildResult,
) -> Result<JsValue, JsValue> {
    let object = js_sys::Object::new();
    set_js_property(
        &object,
        "partialTokenTransactionBytes",
        &js_bytes(&result.partial_token_transaction_bytes),
    )?;
    set_js_property(
        &object,
        "partialTokenTransactionHash",
        &js_bytes(&result.partial_token_transaction_hash),
    )?;
    Ok(object.into())
}

#[cfg(target_arch = "wasm32")]
fn prepared_token_invoice_to_js(result: PreparedTokenInvoice) -> Result<JsValue, JsValue> {
    let object = js_sys::Object::new();
    set_js_property(
        &object,
        "sparkInvoiceFieldsBytes",
        &js_bytes(&result.spark_invoice_fields_bytes),
    )?;
    set_js_property(
        &object,
        "sparkInvoiceHash",
        &js_bytes(&result.spark_invoice_hash),
    )?;
    set_js_property(
        &object,
        "unsignedSparkAddress",
        &JsValue::from_str(&result.unsigned_spark_address),
    )?;
    Ok(object.into())
}

#[cfg(target_arch = "wasm32")]
#[wasm_bindgen(js_name = construct_partial_transfer_transaction)]
pub fn wasm_construct_partial_transfer_transaction(request: JsValue) -> Result<JsValue, JsValue> {
    let request: WasmTransferBuildRequest =
        serde_wasm_bindgen::from_value(request).map_err(js_error)?;
    let result = construct_partial_transfer_transaction(request.into()).map_err(js_error)?;
    partial_transfer_build_result_to_js(result)
}

#[cfg(target_arch = "wasm32")]
#[wasm_bindgen(js_name = hash_partial_token_transaction)]
pub fn wasm_hash_partial_token_transaction(
    partial_token_transaction_bytes: Vec<u8>,
) -> Result<Vec<u8>, JsValue> {
    hash_partial_token_transaction(partial_token_transaction_bytes).map_err(js_error)
}

#[cfg(target_arch = "wasm32")]
#[wasm_bindgen(js_name = build_broadcast_transaction_request)]
pub fn wasm_build_broadcast_transaction_request(request: JsValue) -> Result<Vec<u8>, JsValue> {
    let request: WasmBroadcastBuildRequest =
        serde_wasm_bindgen::from_value(request).map_err(js_error)?;
    build_broadcast_transaction_request(request.into()).map_err(js_error)
}

#[cfg(target_arch = "wasm32")]
#[wasm_bindgen(js_name = prepare_token_invoice)]
pub fn wasm_prepare_token_invoice(request: JsValue) -> Result<JsValue, JsValue> {
    let request: WasmPrepareTokenInvoiceRequest =
        serde_wasm_bindgen::from_value(request).map_err(js_error)?;
    let result = prepare_token_invoice(request.into()).map_err(js_error)?;
    prepared_token_invoice_to_js(result)
}

#[cfg(target_arch = "wasm32")]
#[wasm_bindgen(js_name = finalize_token_invoice)]
pub fn wasm_finalize_token_invoice(request: JsValue) -> Result<String, JsValue> {
    let request: WasmFinalizeTokenInvoiceRequest =
        serde_wasm_bindgen::from_value(request).map_err(js_error)?;
    finalize_token_invoice(request.into()).map_err(js_error)
}
