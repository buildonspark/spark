use prost::Message;
use prost_types::Timestamp;
use sha2::{Digest, Sha256};

use crate::{
    proto::spark_token::{
        self, final_token_transaction, partial_token_transaction, FinalTokenOutput,
        FinalTokenTransaction, PartialTokenOutput, PartialTokenTransaction, TokenOutputToSpend,
        TokenTransactionMetadata, TokenTransferInput,
    },
    SparkTokenPrimitivesError,
};

const HASH_BOOL_IDENTIFIER: u8 = b'b';
const HASH_MAP_IDENTIFIER: u8 = b'd';
const HASH_INT_IDENTIFIER: u8 = b'i';
const HASH_LIST_IDENTIFIER: u8 = b'l';
const HASH_BYTES_IDENTIFIER: u8 = b'r';
const HASH_UNICODE_IDENTIFIER: u8 = b'u';

pub(crate) fn hash_partial_token_transaction_impl(
    partial_token_transaction_bytes: &[u8],
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let partial_transaction = PartialTokenTransaction::decode(partial_token_transaction_bytes)
        .map_err(|err| {
            SparkTokenPrimitivesError::Spark(format!(
                "failed to decode PartialTokenTransaction: {err}"
            ))
        })?;
    hash_partial_token_transaction(&partial_transaction)
}

pub(super) fn hash_partial_token_transaction(
    partial_transaction: &PartialTokenTransaction,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    hash_partial_token_transaction_message(partial_transaction)
}

pub(crate) fn hash_final_token_transaction_impl(
    final_token_transaction_bytes: &[u8],
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let final_transaction =
        FinalTokenTransaction::decode(final_token_transaction_bytes).map_err(|err| {
            SparkTokenPrimitivesError::Spark(format!(
                "failed to decode FinalTokenTransaction: {err}"
            ))
        })?;
    hash_final_token_transaction(&final_transaction)
}

pub(super) fn hash_final_token_transaction(
    final_transaction: &FinalTokenTransaction,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    hash_final_token_transaction_message(final_transaction)
}

fn hash_partial_token_transaction_message(
    message: &PartialTokenTransaction,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();

    if message.version != 0 {
        fields.push(field_hash(1, hash_uint64(message.version as u64)));
    }
    if let Some(metadata) = &message.token_transaction_metadata {
        fields.push(field_hash(
            2,
            hash_token_transaction_metadata_message(metadata)?,
        ));
    }
    if let Some(token_inputs) = &message.token_inputs {
        match token_inputs {
            partial_token_transaction::TokenInputs::MintInput(mint_input) => {
                fields.push(field_hash(3, hash_token_mint_input_message(mint_input)?));
            }
            partial_token_transaction::TokenInputs::TransferInput(transfer_input) => {
                fields.push(field_hash(
                    4,
                    hash_token_transfer_input_message(transfer_input)?,
                ));
            }
            partial_token_transaction::TokenInputs::CreateInput(create_input) => {
                fields.push(field_hash(
                    5,
                    hash_token_create_input_message(create_input)?,
                ));
            }
        }
    }
    if !message.partial_token_outputs.is_empty() {
        let item_hashes = message
            .partial_token_outputs
            .iter()
            .map(hash_partial_token_output_message)
            .collect::<Result<Vec<_>, _>>()?;
        fields.push(field_hash(6, hash_list(item_hashes)));
    }
    if let Some(execute_before) = &message.execute_before {
        fields.push(field_hash(7, hash_timestamp_message(execute_before)));
    }

    Ok(hash_map(fields))
}

fn hash_final_token_transaction_message(
    message: &FinalTokenTransaction,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();

    if message.version != 0 {
        fields.push(field_hash(1, hash_uint64(message.version as u64)));
    }
    if let Some(metadata) = &message.token_transaction_metadata {
        fields.push(field_hash(
            2,
            hash_token_transaction_metadata_message(metadata)?,
        ));
    }
    if let Some(token_inputs) = &message.token_inputs {
        match token_inputs {
            final_token_transaction::TokenInputs::MintInput(mint_input) => {
                fields.push(field_hash(3, hash_token_mint_input_message(mint_input)?));
            }
            final_token_transaction::TokenInputs::TransferInput(transfer_input) => {
                fields.push(field_hash(
                    4,
                    hash_token_transfer_input_message(transfer_input)?,
                ));
            }
            final_token_transaction::TokenInputs::CreateInput(create_input) => {
                fields.push(field_hash(
                    5,
                    hash_token_create_input_message(create_input)?,
                ));
            }
        }
    }
    if !message.final_token_outputs.is_empty() {
        let item_hashes = message
            .final_token_outputs
            .iter()
            .map(hash_final_token_output_message)
            .collect::<Result<Vec<_>, _>>()?;
        fields.push(field_hash(6, hash_list(item_hashes)));
    }
    if let Some(execute_before) = &message.execute_before {
        fields.push(field_hash(7, hash_timestamp_message(execute_before)));
    }

    Ok(hash_map(fields))
}

fn hash_final_token_output_message(
    output: &FinalTokenOutput,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();
    if let Some(partial_token_output) = &output.partial_token_output {
        fields.push(field_hash(
            1,
            hash_partial_token_output_message(partial_token_output)?,
        ));
    }
    if !output.revocation_commitment.is_empty() {
        fields.push(field_hash(2, hash_bytes(&output.revocation_commitment)));
    }
    Ok(hash_map(fields))
}

fn hash_token_transaction_metadata_message(
    metadata: &TokenTransactionMetadata,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();

    if !metadata.spark_operator_identity_public_keys.is_empty() {
        let hashes = metadata
            .spark_operator_identity_public_keys
            .iter()
            .map(|key| hash_bytes(key))
            .collect::<Vec<_>>();
        fields.push(field_hash(2, hash_list(hashes)));
    }
    if metadata.network != 0 {
        fields.push(field_hash(3, hash_int64(metadata.network as i64)));
    }
    if let Some(timestamp) = &metadata.client_created_timestamp {
        fields.push(field_hash(4, hash_timestamp_message(timestamp)));
    }
    if metadata.validity_duration_seconds != 0 {
        fields.push(field_hash(
            5,
            hash_uint64(metadata.validity_duration_seconds),
        ));
    }
    if !metadata.invoice_attachments.is_empty() {
        let hashes = metadata
            .invoice_attachments
            .iter()
            .map(hash_invoice_attachment_message)
            .collect::<Result<Vec<_>, _>>()?;
        fields.push(field_hash(6, hash_list(hashes)));
    }

    Ok(hash_map(fields))
}

fn hash_token_transfer_input_message(
    transfer_input: &TokenTransferInput,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();
    if !transfer_input.outputs_to_spend.is_empty() {
        let hashes = transfer_input
            .outputs_to_spend
            .iter()
            .map(hash_token_output_to_spend_message)
            .collect::<Result<Vec<_>, _>>()?;
        fields.push(field_hash(1, hash_list(hashes)));
    }
    Ok(hash_map(fields))
}

fn hash_token_output_to_spend_message(
    output: &TokenOutputToSpend,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();
    if !output.prev_token_transaction_hash.is_empty() {
        fields.push(field_hash(
            1,
            hash_bytes(&output.prev_token_transaction_hash),
        ));
    }
    if output.prev_token_transaction_vout != 0 {
        fields.push(field_hash(
            2,
            hash_uint64(output.prev_token_transaction_vout as u64),
        ));
    }
    Ok(hash_map(fields))
}

fn hash_partial_token_output_message(
    output: &PartialTokenOutput,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();
    if !output.owner_public_key.is_empty() {
        fields.push(field_hash(1, hash_bytes(&output.owner_public_key)));
    }
    if output.withdraw_bond_sats != 0 {
        fields.push(field_hash(2, hash_uint64(output.withdraw_bond_sats)));
    }
    if output.withdraw_relative_block_locktime != 0 {
        fields.push(field_hash(
            3,
            hash_uint64(output.withdraw_relative_block_locktime),
        ));
    }
    if !output.token_identifier.is_empty() {
        fields.push(field_hash(4, hash_bytes(&output.token_identifier)));
    }
    if !output.token_amount.is_empty() {
        fields.push(field_hash(5, hash_bytes(&output.token_amount)));
    }
    Ok(hash_map(fields))
}

fn hash_token_mint_input_message(
    input: &spark_token::TokenMintInput,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();
    if !input.issuer_public_key.is_empty() {
        fields.push(field_hash(1, hash_bytes(&input.issuer_public_key)));
    }
    if let Some(token_identifier) = &input.token_identifier {
        if !token_identifier.is_empty() {
            fields.push(field_hash(2, hash_bytes(token_identifier)));
        }
    }
    Ok(hash_map(fields))
}

fn hash_token_create_input_message(
    input: &spark_token::TokenCreateInput,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();
    if !input.issuer_public_key.is_empty() {
        fields.push(field_hash(1, hash_bytes(&input.issuer_public_key)));
    }
    if !input.token_name.is_empty() {
        fields.push(field_hash(2, hash_unicode(&input.token_name)));
    }
    if !input.token_ticker.is_empty() {
        fields.push(field_hash(3, hash_unicode(&input.token_ticker)));
    }
    if input.decimals != 0 {
        fields.push(field_hash(4, hash_uint64(input.decimals as u64)));
    }
    if !input.max_supply.is_empty() {
        fields.push(field_hash(5, hash_bytes(&input.max_supply)));
    }
    if input.is_freezable {
        fields.push(field_hash(6, hash_bool(input.is_freezable)));
    }
    if let Some(creation_entity_public_key) = &input.creation_entity_public_key {
        if !creation_entity_public_key.is_empty() {
            fields.push(field_hash(7, hash_bytes(creation_entity_public_key)));
        }
    }
    if let Some(extra_metadata) = &input.extra_metadata {
        if !extra_metadata.is_empty() {
            fields.push(field_hash(8, hash_bytes(extra_metadata)));
        }
    }
    Ok(hash_map(fields))
}

fn hash_invoice_attachment_message(
    invoice: &spark_token::InvoiceAttachment,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut fields = Vec::new();
    if !invoice.spark_invoice.is_empty() {
        fields.push(field_hash(1, hash_unicode(&invoice.spark_invoice)));
    }
    Ok(hash_map(fields))
}

fn hash_timestamp_message(timestamp: &Timestamp) -> Vec<u8> {
    hash_list(vec![
        hash_int64(timestamp.seconds),
        hash_int64(timestamp.nanos as i64),
    ])
}

fn field_hash(field_number: u32, value_hash: Vec<u8>) -> Vec<u8> {
    let mut field = Vec::with_capacity(64);
    field.extend_from_slice(&hash_int64(field_number as i64));
    field.extend_from_slice(&value_hash);
    field
}

fn hash_map(fields: Vec<Vec<u8>>) -> Vec<u8> {
    let total_len = fields.iter().map(Vec::len).sum();
    let mut data = Vec::with_capacity(total_len);
    for field in fields {
        data.extend_from_slice(&field);
    }
    hash_with_prefix(HASH_MAP_IDENTIFIER, &data)
}

fn hash_list(items: Vec<Vec<u8>>) -> Vec<u8> {
    let total_len = items.iter().map(Vec::len).sum();
    let mut data = Vec::with_capacity(total_len);
    for item in items {
        data.extend_from_slice(&item);
    }
    hash_with_prefix(HASH_LIST_IDENTIFIER, &data)
}

fn hash_bool(value: bool) -> Vec<u8> {
    let payload = if value { [b'1'] } else { [b'0'] };
    hash_with_prefix(HASH_BOOL_IDENTIFIER, &payload)
}

fn hash_int64(value: i64) -> Vec<u8> {
    hash_with_prefix(HASH_INT_IDENTIFIER, &value.to_be_bytes())
}

fn hash_uint64(value: u64) -> Vec<u8> {
    hash_with_prefix(HASH_INT_IDENTIFIER, &value.to_be_bytes())
}

fn hash_bytes(value: &[u8]) -> Vec<u8> {
    hash_with_prefix(HASH_BYTES_IDENTIFIER, value)
}

fn hash_unicode(value: &str) -> Vec<u8> {
    hash_with_prefix(HASH_UNICODE_IDENTIFIER, value.as_bytes())
}

fn hash_with_prefix(prefix: u8, data: &[u8]) -> Vec<u8> {
    let mut hasher = Sha256::new();
    hasher.update([prefix]);
    hasher.update(data);
    hasher.finalize().to_vec()
}

pub(super) fn hex_string(bytes: &[u8]) -> String {
    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        use std::fmt::Write as _;
        let _ = write!(output, "{byte:02x}");
    }
    output
}
