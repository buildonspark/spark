use std::collections::BTreeMap;

use bech32::Hrp;
use prost::Message;
use prost_types::Timestamp;
use sha2::{Digest, Sha256};

use crate::{
    proto::{
        multisig,
        spark::{self, Network, SparkAddress},
        spark_token::{
            self, partial_token_transaction, signature_with_index, BroadcastTransactionRequest,
            PartialTokenOutput, PartialTokenTransaction, SignatureWithIndex, TokenOutputToSpend,
            TokenTransactionMetadata, TokenTransferInput,
        },
    },
    BroadcastBuildRequest, PartialTransferBuildResult, ReceiverTokenOutput, SelectedTokenOutput,
    SignatureWithIndexInput, SparkTokenPrimitivesError, TransferBuildRequest,
};

const HASH_BOOL_IDENTIFIER: u8 = b'b';
const HASH_MAP_IDENTIFIER: u8 = b'd';
const HASH_INT_IDENTIFIER: u8 = b'i';
const HASH_LIST_IDENTIFIER: u8 = b'l';
const HASH_BYTES_IDENTIFIER: u8 = b'r';
const HASH_UNICODE_IDENTIFIER: u8 = b'u';
const MAX_TOKEN_OUTPUTS_PER_TRANSACTION: usize = 500;

pub(crate) fn construct_partial_transfer_transaction_impl(
    request: TransferBuildRequest,
) -> Result<PartialTransferBuildResult, SparkTokenPrimitivesError> {
    validate_transfer_request(&request)?;

    let mut selected_outputs = request.selected_outputs;
    if selected_outputs.len() > MAX_TOKEN_OUTPUTS_PER_TRANSACTION {
        return Err(format!(
            "cannot transfer more than {MAX_TOKEN_OUTPUTS_PER_TRANSACTION} inputs in a single transaction"
        )
        .into());
    }
    selected_outputs.sort_by_key(|output| output.previous_transaction_vout);

    let mut available_by_token = BTreeMap::<Vec<u8>, u128>::new();
    let mut token_order = Vec::<Vec<u8>>::new();

    for output in &selected_outputs {
        let amount = decode_u128_be(&output.token_amount, "selected_outputs.token_amount")?;
        if amount == 0 {
            return Err("selected output token amount must be greater than zero".into());
        }

        let entry = available_by_token
            .entry(output.token_identifier.clone())
            .or_insert_with(|| {
                token_order.push(output.token_identifier.clone());
                0
            });
        *entry += amount;
    }

    let mut requested_by_token = BTreeMap::<Vec<u8>, u128>::new();
    let mut partial_outputs =
        Vec::<PartialTokenOutput>::with_capacity(request.receiver_outputs.len());
    let mut invoice_attachments = Vec::<spark_token::InvoiceAttachment>::new();
    for output in &request.receiver_outputs {
        let resolved_output = resolve_receiver_output(output, request.network)?;
        let amount = decode_u128_be(
            &resolved_output.token_amount,
            "receiver_outputs.token_amount",
        )?;
        if amount == 0 {
            return Err("receiver output token amount must be greater than zero".into());
        }
        *requested_by_token
            .entry(resolved_output.token_identifier.clone())
            .or_default() += amount;
        partial_outputs.push(build_partial_output(
            resolved_output.receiver_public_key,
            request.withdraw_bond_sats,
            request.withdraw_relative_block_locktime,
            resolved_output.token_identifier,
            resolved_output.token_amount,
        ));
        if let Some(spark_invoice) = resolved_output.spark_invoice {
            invoice_attachments.push(spark_token::InvoiceAttachment { spark_invoice });
        }
    }

    for (token_identifier, requested_amount) in &requested_by_token {
        let available_amount = available_by_token
            .get(token_identifier)
            .copied()
            .unwrap_or(0);
        if available_amount < *requested_amount {
            return Err(format!(
                "insufficient input amount for token {}: available={}, requested={requested_amount}",
                hex_string(token_identifier),
                available_amount
            )
            .into());
        }
    }

    for token_identifier in token_order {
        let available_amount = available_by_token
            .get(&token_identifier)
            .copied()
            .unwrap_or(0);
        let requested_amount = requested_by_token
            .get(&token_identifier)
            .copied()
            .unwrap_or(0);
        if available_amount > requested_amount {
            partial_outputs.push(build_partial_output(
                request.identity_public_key.clone(),
                request.withdraw_bond_sats,
                request.withdraw_relative_block_locktime,
                token_identifier,
                encode_u128_be(available_amount - requested_amount),
            ));
        }
    }

    if partial_outputs.len() > MAX_TOKEN_OUTPUTS_PER_TRANSACTION {
        return Err(format!(
            "cannot create more than {MAX_TOKEN_OUTPUTS_PER_TRANSACTION} token outputs in a single transaction"
        )
        .into());
    }

    let mut operator_identity_public_keys = request.operator_identity_public_keys;
    operator_identity_public_keys.sort();
    validate_sorted_unique_operator_keys(&operator_identity_public_keys)?;
    invoice_attachments.sort_by(|a, b| a.spark_invoice.cmp(&b.spark_invoice));

    let partial_transaction = PartialTokenTransaction {
        version: 3,
        token_transaction_metadata: Some(TokenTransactionMetadata {
            spark_operator_identity_public_keys: operator_identity_public_keys,
            network: request.network as i32,
            client_created_timestamp: Some(timestamp_from_unix_micros(
                request.client_created_timestamp_unix_micros,
            )?),
            validity_duration_seconds: request.validity_duration_seconds,
            invoice_attachments,
        }),
        token_inputs: Some(partial_token_transaction::TokenInputs::TransferInput(
            TokenTransferInput {
                outputs_to_spend: selected_outputs
                    .into_iter()
                    .map(|output| TokenOutputToSpend {
                        prev_token_transaction_hash: output.previous_transaction_hash,
                        prev_token_transaction_vout: output.previous_transaction_vout,
                    })
                    .collect(),
            },
        )),
        partial_token_outputs: partial_outputs,
        execute_before: None,
    };

    let partial_token_transaction_hash = hash_partial_token_transaction(&partial_transaction)?;

    Ok(PartialTransferBuildResult {
        partial_token_transaction_bytes: partial_transaction.encode_to_vec(),
        partial_token_transaction_hash,
    })
}

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

pub(crate) fn build_broadcast_transaction_request_impl(
    request: BroadcastBuildRequest,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    validate_length(&request.identity_public_key, 33, "identity_public_key")?;

    let partial_transaction =
        PartialTokenTransaction::decode(request.partial_token_transaction_bytes.as_slice())
            .map_err(|err| {
                SparkTokenPrimitivesError::Spark(format!(
                    "failed to decode PartialTokenTransaction for broadcast request: {err}"
                ))
            })?;

    validate_broadcast_owner_signatures(&partial_transaction, &request.owner_signatures)?;

    let owner_signatures = request
        .owner_signatures
        .into_iter()
        .map(signature_with_index_from_input)
        .collect::<Result<Vec<_>, _>>()?;

    let broadcast_request = BroadcastTransactionRequest {
        identity_public_key: request.identity_public_key,
        partial_token_transaction: Some(partial_transaction),
        token_transaction_owner_signatures: owner_signatures,
    };

    Ok(broadcast_request.encode_to_vec())
}

fn validate_broadcast_owner_signatures(
    partial_transaction: &PartialTokenTransaction,
    owner_signatures: &[SignatureWithIndexInput],
) -> Result<(), SparkTokenPrimitivesError> {
    let token_inputs = partial_transaction.token_inputs.as_ref().ok_or_else(|| {
        SparkTokenPrimitivesError::Spark(
            "partial token transaction is missing token_inputs".to_owned(),
        )
    })?;

    match token_inputs {
        partial_token_transaction::TokenInputs::CreateInput(_) => {
            validate_exactly_one_index_zero_signature(owner_signatures, "createInput")
        }
        partial_token_transaction::TokenInputs::MintInput(_) => {
            validate_exactly_one_index_zero_signature(owner_signatures, "mintInput")
        }
        partial_token_transaction::TokenInputs::TransferInput(transfer_input) => {
            validate_transfer_owner_signatures(
                owner_signatures,
                transfer_input.outputs_to_spend.len(),
            )
        }
    }
}

fn validate_exactly_one_index_zero_signature(
    owner_signatures: &[SignatureWithIndexInput],
    input_case: &str,
) -> Result<(), SparkTokenPrimitivesError> {
    if owner_signatures.len() != 1 {
        return Err(format!(
            "{input_case} partial token transaction requires exactly one owner signature"
        )
        .into());
    }
    if owner_signatures[0].input_index != 0 {
        return Err(format!(
            "{input_case} owner signature must use input_index 0, got {}",
            owner_signatures[0].input_index
        )
        .into());
    }
    Ok(())
}

fn validate_transfer_owner_signatures(
    owner_signatures: &[SignatureWithIndexInput],
    expected_inputs: usize,
) -> Result<(), SparkTokenPrimitivesError> {
    if owner_signatures.len() != expected_inputs {
        return Err(format!(
            "transfer partial token transaction requires exactly {expected_inputs} owner signatures, got {}",
            owner_signatures.len()
        )
        .into());
    }

    let mut seen = vec![false; expected_inputs];
    for signature in owner_signatures {
        let index = signature.input_index as usize;
        if index >= expected_inputs {
            return Err(format!(
                "owner signature input_index {} is out of range for {expected_inputs} transfer inputs",
                signature.input_index
            )
            .into());
        }
        if seen[index] {
            return Err(format!(
                "duplicate owner signature for input_index {}",
                signature.input_index
            )
            .into());
        }
        seen[index] = true;
    }

    for (index, present) in seen.into_iter().enumerate() {
        if !present {
            return Err(format!(
                "missing owner signature for input_index {index}, indexes must be contiguous"
            )
            .into());
        }
    }

    Ok(())
}

fn validate_transfer_request(
    request: &TransferBuildRequest,
) -> Result<(), SparkTokenPrimitivesError> {
    validate_length(&request.identity_public_key, 33, "identity_public_key")?;

    if request.selected_outputs.is_empty() {
        return Err("selected_outputs must not be empty".into());
    }
    if request.receiver_outputs.is_empty() {
        return Err("receiver_outputs must not be empty".into());
    }
    if request.validity_duration_seconds == 0 || request.validity_duration_seconds > 300 {
        return Err("validity_duration_seconds must be between 1 and 300".into());
    }
    if request.withdraw_bond_sats == 0 {
        return Err("withdraw_bond_sats must be greater than zero".into());
    }
    if request.withdraw_relative_block_locktime == 0 {
        return Err("withdraw_relative_block_locktime must be greater than zero".into());
    }
    if Network::try_from(request.network as i32).is_err()
        || request.network == Network::Unspecified as u32
    {
        return Err(format!("invalid spark network value: {}", request.network).into());
    }

    for (index, output) in request.selected_outputs.iter().enumerate() {
        validate_selected_output(output, index)?;
    }
    for (index, output) in request.receiver_outputs.iter().enumerate() {
        validate_receiver_output(output, index)?;
    }
    for (index, operator_key) in request.operator_identity_public_keys.iter().enumerate() {
        validate_length(
            operator_key,
            33,
            &format!("operator_identity_public_keys[{index}]"),
        )?;
    }

    Ok(())
}

fn validate_selected_output(
    output: &SelectedTokenOutput,
    index: usize,
) -> Result<(), SparkTokenPrimitivesError> {
    validate_length(
        &output.previous_transaction_hash,
        32,
        &format!("selected_outputs[{index}].previous_transaction_hash"),
    )?;
    validate_length(
        &output.owner_public_key,
        33,
        &format!("selected_outputs[{index}].owner_public_key"),
    )?;
    validate_length(
        &output.token_identifier,
        32,
        &format!("selected_outputs[{index}].token_identifier"),
    )?;
    validate_length(
        &output.token_amount,
        16,
        &format!("selected_outputs[{index}].token_amount"),
    )?;
    Ok(())
}

fn validate_receiver_output(
    output: &ReceiverTokenOutput,
    index: usize,
) -> Result<(), SparkTokenPrimitivesError> {
    if output.receiver_spark_address.is_empty() {
        return Err(
            format!("receiver_outputs[{index}].receiver_spark_address must not be empty").into(),
        );
    }
    if let Some(token_identifier) = &output.token_identifier {
        validate_length(
            token_identifier,
            32,
            &format!("receiver_outputs[{index}].token_identifier"),
        )?;
    }
    if let Some(token_amount) = &output.token_amount {
        validate_length(
            token_amount,
            16,
            &format!("receiver_outputs[{index}].token_amount"),
        )?;
    }
    Ok(())
}

struct ResolvedReceiverOutput {
    receiver_public_key: Vec<u8>,
    token_identifier: Vec<u8>,
    token_amount: Vec<u8>,
    spark_invoice: Option<String>,
}

fn resolve_receiver_output(
    output: &ReceiverTokenOutput,
    expected_network: u32,
) -> Result<ResolvedReceiverOutput, SparkTokenPrimitivesError> {
    let (hrp, data) = bech32::decode(&output.receiver_spark_address).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("failed to decode receiver_spark_address: {err}"))
    })?;
    let expected_hrp = network_to_primary_hrp(expected_network)?;
    let hrp_str = hrp.as_str().to_ascii_lowercase();
    if !matches_network_hrp(&hrp_str, expected_network) {
        return Err(format!(
            "receiver_spark_address network mismatch: expected {expected_hrp}, got {}",
            hrp.as_str()
        )
        .into());
    }

    let spark_address = SparkAddress::decode(data.as_slice()).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!(
            "failed to decode SparkAddress from receiver_spark_address: {err}"
        ))
    })?;
    validate_length(
        &spark_address.identity_public_key,
        33,
        "receiver_outputs.receiver_spark_address.identity_public_key",
    )?;

    let receiver_public_key = spark_address.identity_public_key;
    let explicit_token_identifier = output.token_identifier.clone();
    let explicit_token_amount = output.token_amount.clone();
    let mut spark_invoice = None;

    let (token_identifier, token_amount) = if let Some(invoice_fields) =
        spark_address.spark_invoice_fields
    {
        let payment = invoice_fields.payment_type.ok_or_else(|| {
            SparkTokenPrimitivesError::Spark(
                "receiver_spark_address invoice is missing payment_type".to_owned(),
            )
        })?;
        let tokens_payment = match payment {
            spark::spark_invoice_fields::PaymentType::TokensPayment(tokens_payment) => {
                tokens_payment
            }
            spark::spark_invoice_fields::PaymentType::SatsPayment(_) => {
                return Err(
                    "receiver_spark_address invoice is a sats invoice, expected token invoice"
                        .into(),
                )
            }
        };

        let token_identifier = resolve_invoice_or_explicit_bytes(
            explicit_token_identifier,
            tokens_payment.token_identifier,
            32,
            "token_identifier",
        )?;
        let token_amount = resolve_invoice_or_explicit_bytes(
            explicit_token_amount,
            tokens_payment.amount,
            16,
            "token_amount",
        )?;
        spark_invoice = Some(output.receiver_spark_address.clone());
        (token_identifier, token_amount)
    } else {
        let token_identifier = explicit_token_identifier.ok_or_else(|| {
            SparkTokenPrimitivesError::Spark(
                "receiver_outputs.token_identifier is required for non-invoice receiver_spark_address"
                    .to_owned(),
            )
        })?;
        let token_amount = explicit_token_amount.ok_or_else(|| {
            SparkTokenPrimitivesError::Spark(
                "receiver_outputs.token_amount is required for non-invoice receiver_spark_address"
                    .to_owned(),
            )
        })?;
        (token_identifier, token_amount)
    };

    validate_length(&token_identifier, 32, "resolved receiver token_identifier")?;
    validate_length(&token_amount, 16, "resolved receiver token_amount")?;

    Ok(ResolvedReceiverOutput {
        receiver_public_key,
        token_identifier,
        token_amount,
        spark_invoice,
    })
}

fn resolve_invoice_or_explicit_bytes(
    explicit: Option<Vec<u8>>,
    embedded: Option<Vec<u8>>,
    expected_len: usize,
    field_name: &str,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    if let Some(ref explicit_bytes) = explicit {
        validate_length(explicit_bytes, expected_len, field_name)?;
    }
    if let Some(ref embedded_bytes) = embedded {
        validate_length(embedded_bytes, expected_len, field_name)?;
    }

    match (explicit, embedded) {
        (Some(explicit_bytes), Some(embedded_bytes)) => {
            if explicit_bytes != embedded_bytes {
                Err(format!(
                    "{field_name} mismatch between explicit receiver output and embedded invoice"
                )
                .into())
            } else {
                Ok(explicit_bytes)
            }
        }
        (Some(explicit_bytes), None) => Ok(explicit_bytes),
        (None, Some(embedded_bytes)) => Ok(embedded_bytes),
        (None, None) => Err(format!(
            "{field_name} is required either explicitly or in receiver_spark_address invoice"
        )
        .into()),
    }
}

fn matches_network_hrp(hrp: &str, network: u32) -> bool {
    match network {
        x if x == Network::Regtest as u32 => matches!(hrp, "sparkrt" | "sparkl" | "sprt" | "spl"),
        x if x == Network::Testnet as u32 => matches!(hrp, "sparkt" | "spt"),
        x if x == Network::Signet as u32 => matches!(hrp, "sparks" | "sps"),
        x if x == Network::Mainnet as u32 => matches!(hrp, "spark" | "sp"),
        _ => false,
    }
}

fn network_to_primary_hrp(network: u32) -> Result<Hrp, SparkTokenPrimitivesError> {
    let hrp = match network {
        x if x == Network::Regtest as u32 => "sparkrt",
        x if x == Network::Testnet as u32 => "sparkt",
        x if x == Network::Signet as u32 => "sparks",
        x if x == Network::Mainnet as u32 => "spark",
        _ => return Err(format!("invalid spark network value: {network}").into()),
    };
    Hrp::parse(hrp).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("invalid internal hrp {hrp}: {err}"))
    })
}

fn validate_sorted_unique_operator_keys(
    operator_keys: &[Vec<u8>],
) -> Result<(), SparkTokenPrimitivesError> {
    for window in operator_keys.windows(2) {
        if window[0] >= window[1] {
            return Err("operator_identity_public_keys must be strictly bytewise ascending".into());
        }
    }
    Ok(())
}

fn build_partial_output(
    owner_public_key: Vec<u8>,
    withdraw_bond_sats: u64,
    withdraw_relative_block_locktime: u64,
    token_identifier: Vec<u8>,
    token_amount: Vec<u8>,
) -> PartialTokenOutput {
    PartialTokenOutput {
        owner_public_key,
        withdraw_bond_sats,
        withdraw_relative_block_locktime,
        token_identifier,
        token_amount,
    }
}

fn signature_with_index_from_input(
    input: SignatureWithIndexInput,
) -> Result<SignatureWithIndex, SparkTokenPrimitivesError> {
    validate_length(&input.public_key, 33, "owner_signatures.public_key")?;
    if input.signature.len() < 64 || input.signature.len() > 73 {
        return Err("owner_signatures.signature must be between 64 and 73 bytes".into());
    }

    Ok(SignatureWithIndex {
        signature: None,
        input_index: input.input_index,
        authority_signatures: Some(signature_with_index::AuthoritySignatures::SingleSignature(
            multisig::KeyedSignature {
                public_key: input.public_key,
                signature: input.signature,
            },
        )),
    })
}

fn validate_length(
    bytes: &[u8],
    expected_len: usize,
    field_name: &str,
) -> Result<(), SparkTokenPrimitivesError> {
    if bytes.len() != expected_len {
        return Err(format!(
            "{field_name} must be {expected_len} bytes, got {}",
            bytes.len()
        )
        .into());
    }
    Ok(())
}

fn decode_u128_be(bytes: &[u8], field_name: &str) -> Result<u128, SparkTokenPrimitivesError> {
    validate_length(bytes, 16, field_name)?;
    let mut buf = [0_u8; 16];
    buf.copy_from_slice(bytes);
    Ok(u128::from_be_bytes(buf))
}

fn encode_u128_be(value: u128) -> Vec<u8> {
    value.to_be_bytes().to_vec()
}

fn timestamp_from_unix_micros(unix_micros: i64) -> Result<Timestamp, SparkTokenPrimitivesError> {
    let seconds = unix_micros.div_euclid(1_000_000);
    let micros = unix_micros.rem_euclid(1_000_000);
    let nanos = micros.checked_mul(1_000).ok_or_else(|| {
        SparkTokenPrimitivesError::Spark("timestamp micros overflowed".to_owned())
    })?;
    Ok(Timestamp {
        seconds,
        nanos: nanos as i32,
    })
}

fn hash_partial_token_transaction(
    partial_transaction: &PartialTokenTransaction,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    hash_partial_token_transaction_message(partial_transaction)
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

fn hex_string(bytes: &[u8]) -> String {
    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        use std::fmt::Write as _;
        let _ = write!(output, "{byte:02x}");
    }
    output
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::{
        spark::{self, SparkAddress, SparkInvoiceFields, TokensPayment},
        spark_token::partial_token_transaction,
    };
    use base64::{engine::general_purpose::STANDARD, Engine as _};
    use bech32::{Bech32m, Hrp};
    use serde::Deserialize;
    use std::{fs, path::PathBuf};
    use time::{format_description::well_known::Rfc3339, OffsetDateTime};

    fn sample_key(fill: u8) -> Vec<u8> {
        vec![fill; 33]
    }

    fn sample_token(fill: u8) -> Vec<u8> {
        vec![fill; 32]
    }

    fn sample_token_with_index(index: usize) -> Vec<u8> {
        let mut token = vec![0_u8; 32];
        token[30] = (index / 256) as u8;
        token[31] = (index % 256) as u8;
        token
    }

    fn sample_hash(fill: u8) -> Vec<u8> {
        vec![fill; 32]
    }

    fn encode_spark_address(
        receiver_public_key: Vec<u8>,
        network: Network,
        spark_invoice_fields: Option<SparkInvoiceFields>,
    ) -> String {
        let spark_address = SparkAddress {
            identity_public_key: receiver_public_key,
            spark_invoice_fields,
            signature: None,
        };
        let hrp = match network {
            Network::Regtest => Hrp::parse("sparkrt").unwrap(),
            Network::Testnet => Hrp::parse("sparkt").unwrap(),
            Network::Signet => Hrp::parse("sparks").unwrap(),
            Network::Mainnet => Hrp::parse("spark").unwrap(),
            Network::Unspecified => panic!("unsupported network"),
        };
        bech32::encode::<Bech32m>(hrp, &spark_address.encode_to_vec()).unwrap()
    }

    #[test]
    fn construct_partial_transfer_transaction_adds_change() {
        let request = TransferBuildRequest {
            identity_public_key: sample_key(0x01),
            selected_outputs: vec![
                SelectedTokenOutput {
                    previous_transaction_hash: sample_hash(0xaa),
                    previous_transaction_vout: 2,
                    owner_public_key: sample_key(0x01),
                    token_identifier: sample_token(0x10),
                    token_amount: encode_u128_be(100),
                },
                SelectedTokenOutput {
                    previous_transaction_hash: sample_hash(0xbb),
                    previous_transaction_vout: 0,
                    owner_public_key: sample_key(0x01),
                    token_identifier: sample_token(0x10),
                    token_amount: encode_u128_be(25),
                },
            ],
            receiver_outputs: vec![ReceiverTokenOutput {
                receiver_spark_address: encode_spark_address(
                    sample_key(0x02),
                    Network::Regtest,
                    None,
                ),
                token_identifier: Some(sample_token(0x10)),
                token_amount: Some(encode_u128_be(80)),
            }],
            operator_identity_public_keys: vec![sample_key(0x04), sample_key(0x03)],
            network: Network::Regtest as u32,
            validity_duration_seconds: 60,
            client_created_timestamp_unix_micros: 100_000,
            withdraw_bond_sats: 10_000,
            withdraw_relative_block_locktime: 100,
        };

        let result = construct_partial_transfer_transaction_impl(request).unwrap();
        let partial =
            PartialTokenTransaction::decode(result.partial_token_transaction_bytes.as_slice())
                .unwrap();

        let transfer_input = match partial.token_inputs.unwrap() {
            partial_token_transaction::TokenInputs::TransferInput(transfer_input) => transfer_input,
            _ => panic!("expected transfer input"),
        };

        assert_eq!(transfer_input.outputs_to_spend.len(), 2);
        assert_eq!(
            transfer_input.outputs_to_spend[0].prev_token_transaction_vout,
            0
        );
        assert_eq!(
            transfer_input.outputs_to_spend[1].prev_token_transaction_vout,
            2
        );

        assert_eq!(partial.partial_token_outputs.len(), 2);
        assert_eq!(
            decode_u128_be(
                &partial.partial_token_outputs[0].token_amount,
                "token_amount"
            )
            .unwrap(),
            80
        );
        assert_eq!(
            decode_u128_be(
                &partial.partial_token_outputs[1].token_amount,
                "token_amount"
            )
            .unwrap(),
            45
        );

        let metadata = partial.token_transaction_metadata.unwrap();
        assert_eq!(
            metadata.spark_operator_identity_public_keys,
            vec![sample_key(0x03), sample_key(0x04)]
        );
        assert!(metadata.invoice_attachments.is_empty());
        assert_eq!(result.partial_token_transaction_hash.len(), 32);
    }

    #[test]
    fn construct_partial_transfer_transaction_rejects_insufficient_amount() {
        let request = TransferBuildRequest {
            identity_public_key: sample_key(0x01),
            selected_outputs: vec![SelectedTokenOutput {
                previous_transaction_hash: sample_hash(0xaa),
                previous_transaction_vout: 0,
                owner_public_key: sample_key(0x01),
                token_identifier: sample_token(0x10),
                token_amount: encode_u128_be(50),
            }],
            receiver_outputs: vec![ReceiverTokenOutput {
                receiver_spark_address: encode_spark_address(
                    sample_key(0x02),
                    Network::Regtest,
                    None,
                ),
                token_identifier: Some(sample_token(0x10)),
                token_amount: Some(encode_u128_be(80)),
            }],
            operator_identity_public_keys: vec![sample_key(0x03)],
            network: Network::Regtest as u32,
            validity_duration_seconds: 60,
            client_created_timestamp_unix_micros: 100_000,
            withdraw_bond_sats: 10_000,
            withdraw_relative_block_locktime: 100,
        };

        let error = construct_partial_transfer_transaction_impl(request).unwrap_err();
        assert!(error.to_string().contains("insufficient input amount"));
    }

    #[test]
    fn construct_partial_transfer_transaction_rejects_too_many_inputs_total() {
        let request = TransferBuildRequest {
            identity_public_key: sample_key(0x01),
            selected_outputs: (0..501)
                .map(|index| SelectedTokenOutput {
                    previous_transaction_hash: sample_hash((index % 255) as u8),
                    previous_transaction_vout: index as u32,
                    owner_public_key: sample_key(0x01),
                    token_identifier: if index % 2 == 0 {
                        sample_token(0x10)
                    } else {
                        sample_token(0x11)
                    },
                    token_amount: encode_u128_be(1),
                })
                .collect(),
            receiver_outputs: vec![ReceiverTokenOutput {
                receiver_spark_address: encode_spark_address(
                    sample_key(0x02),
                    Network::Regtest,
                    None,
                ),
                token_identifier: Some(sample_token(0x10)),
                token_amount: Some(encode_u128_be(1)),
            }],
            operator_identity_public_keys: vec![sample_key(0x03)],
            network: Network::Regtest as u32,
            validity_duration_seconds: 60,
            client_created_timestamp_unix_micros: 100_000,
            withdraw_bond_sats: 10_000,
            withdraw_relative_block_locktime: 100,
        };

        let error = construct_partial_transfer_transaction_impl(request).unwrap_err();
        assert!(error
            .to_string()
            .contains("cannot transfer more than 500 inputs"));
    }

    #[test]
    fn construct_partial_transfer_transaction_rejects_too_many_outputs_total() {
        let request = TransferBuildRequest {
            identity_public_key: sample_key(0x01),
            selected_outputs: (0..500)
                .map(|index| SelectedTokenOutput {
                    previous_transaction_hash: sample_hash((index % 255) as u8),
                    previous_transaction_vout: index as u32,
                    owner_public_key: sample_key(0x01),
                    token_identifier: sample_token_with_index(index),
                    token_amount: encode_u128_be(2),
                })
                .collect(),
            receiver_outputs: (0..500)
                .map(|index| ReceiverTokenOutput {
                    receiver_spark_address: encode_spark_address(
                        sample_key(0x02),
                        Network::Regtest,
                        None,
                    ),
                    token_identifier: Some(sample_token_with_index(index)),
                    token_amount: Some(encode_u128_be(1)),
                })
                .collect(),
            operator_identity_public_keys: vec![sample_key(0x03)],
            network: Network::Regtest as u32,
            validity_duration_seconds: 60,
            client_created_timestamp_unix_micros: 100_000,
            withdraw_bond_sats: 10_000,
            withdraw_relative_block_locktime: 100,
        };

        let error = construct_partial_transfer_transaction_impl(request).unwrap_err();
        assert!(error
            .to_string()
            .contains("cannot create more than 500 token outputs"));
    }

    #[test]
    fn construct_partial_transfer_transaction_extracts_token_invoice_fields() {
        let receiver_public_key = sample_key(0x02);
        let token_identifier = sample_token(0x10);
        let token_amount = encode_u128_be(80);
        let invoice = encode_spark_address(
            receiver_public_key.clone(),
            Network::Regtest,
            Some(SparkInvoiceFields {
                version: 1,
                id: vec![0x44; 16],
                memo: None,
                sender_public_key: None,
                expiry_time: None,
                payment_type: Some(spark::spark_invoice_fields::PaymentType::TokensPayment(
                    TokensPayment {
                        token_identifier: Some(token_identifier.clone()),
                        amount: Some(token_amount.clone()),
                    },
                )),
            }),
        );
        let request = TransferBuildRequest {
            identity_public_key: sample_key(0x01),
            selected_outputs: vec![SelectedTokenOutput {
                previous_transaction_hash: sample_hash(0xaa),
                previous_transaction_vout: 0,
                owner_public_key: sample_key(0x01),
                token_identifier: token_identifier.clone(),
                token_amount: encode_u128_be(100),
            }],
            receiver_outputs: vec![ReceiverTokenOutput {
                receiver_spark_address: invoice.clone(),
                token_identifier: None,
                token_amount: None,
            }],
            operator_identity_public_keys: vec![sample_key(0x03)],
            network: Network::Regtest as u32,
            validity_duration_seconds: 60,
            client_created_timestamp_unix_micros: 100_000,
            withdraw_bond_sats: 10_000,
            withdraw_relative_block_locktime: 100,
        };

        let result = construct_partial_transfer_transaction_impl(request).unwrap();
        let partial =
            PartialTokenTransaction::decode(result.partial_token_transaction_bytes.as_slice())
                .unwrap();

        assert_eq!(
            partial.partial_token_outputs[0].owner_public_key,
            receiver_public_key
        );
        assert_eq!(
            partial.partial_token_outputs[0].token_identifier,
            token_identifier
        );
        assert_eq!(partial.partial_token_outputs[0].token_amount, token_amount);
        let metadata = partial.token_transaction_metadata.unwrap();
        assert_eq!(metadata.invoice_attachments.len(), 1);
        assert_eq!(metadata.invoice_attachments[0].spark_invoice, invoice);
    }

    #[test]
    fn construct_partial_transfer_transaction_rejects_invoice_field_mismatch() {
        let invoice = encode_spark_address(
            sample_key(0x02),
            Network::Regtest,
            Some(SparkInvoiceFields {
                version: 1,
                id: vec![0x44; 16],
                memo: None,
                sender_public_key: None,
                expiry_time: None,
                payment_type: Some(spark::spark_invoice_fields::PaymentType::TokensPayment(
                    TokensPayment {
                        token_identifier: Some(sample_token(0x10)),
                        amount: Some(encode_u128_be(80)),
                    },
                )),
            }),
        );
        let request = TransferBuildRequest {
            identity_public_key: sample_key(0x01),
            selected_outputs: vec![SelectedTokenOutput {
                previous_transaction_hash: sample_hash(0xaa),
                previous_transaction_vout: 0,
                owner_public_key: sample_key(0x01),
                token_identifier: sample_token(0x10),
                token_amount: encode_u128_be(100),
            }],
            receiver_outputs: vec![ReceiverTokenOutput {
                receiver_spark_address: invoice,
                token_identifier: Some(sample_token(0x11)),
                token_amount: None,
            }],
            operator_identity_public_keys: vec![sample_key(0x03)],
            network: Network::Regtest as u32,
            validity_duration_seconds: 60,
            client_created_timestamp_unix_micros: 100_000,
            withdraw_bond_sats: 10_000,
            withdraw_relative_block_locktime: 100,
        };

        let error = construct_partial_transfer_transaction_impl(request).unwrap_err();
        assert!(error.to_string().contains("token_identifier mismatch"));
    }

    #[derive(Debug, Deserialize)]
    struct PartialHashCaseFile {
        #[serde(rename = "testCases")]
        test_cases: Vec<PartialHashCase>,
    }

    #[derive(Debug, Deserialize)]
    struct PartialHashCase {
        name: String,
        #[serde(rename = "expectedHash")]
        expected_hash: String,
        #[serde(rename = "partialTokenTransaction")]
        partial_token_transaction: PartialTokenTransactionJson,
    }

    #[derive(Debug, Deserialize)]
    struct PartialTokenTransactionJson {
        version: u32,
        #[serde(rename = "tokenTransactionMetadata")]
        token_transaction_metadata: Option<TokenTransactionMetadataJson>,
        #[serde(rename = "mintInput")]
        mint_input: Option<TokenMintInputJson>,
        #[serde(rename = "transferInput")]
        transfer_input: Option<TokenTransferInputJson>,
        #[serde(rename = "createInput")]
        create_input: Option<TokenCreateInputJson>,
        #[serde(rename = "partialTokenOutputs", default)]
        partial_token_outputs: Vec<PartialTokenOutputJson>,
        #[serde(rename = "executeBefore")]
        execute_before: Option<String>,
    }

    #[derive(Debug, Deserialize)]
    struct TokenTransactionMetadataJson {
        #[serde(rename = "sparkOperatorIdentityPublicKeys", default)]
        spark_operator_identity_public_keys: Vec<String>,
        network: String,
        #[serde(rename = "clientCreatedTimestamp")]
        client_created_timestamp: Option<String>,
        #[serde(rename = "validityDurationSeconds")]
        validity_duration_seconds: Option<String>,
        #[serde(rename = "invoiceAttachments", default)]
        invoice_attachments: Vec<InvoiceAttachmentJson>,
    }

    #[derive(Debug, Deserialize)]
    struct InvoiceAttachmentJson {
        #[serde(rename = "sparkInvoice")]
        spark_invoice: String,
    }

    #[derive(Debug, Deserialize)]
    struct TokenMintInputJson {
        #[serde(rename = "issuerPublicKey")]
        issuer_public_key: String,
        #[serde(rename = "tokenIdentifier")]
        token_identifier: Option<String>,
    }

    #[derive(Debug, Deserialize)]
    struct TokenTransferInputJson {
        #[serde(rename = "outputsToSpend", default)]
        outputs_to_spend: Vec<TokenOutputToSpendJson>,
    }

    #[derive(Debug, Deserialize)]
    struct TokenOutputToSpendJson {
        #[serde(rename = "prevTokenTransactionHash")]
        prev_token_transaction_hash: String,
        #[serde(rename = "prevTokenTransactionVout")]
        prev_token_transaction_vout: u32,
    }

    #[derive(Debug, Deserialize)]
    struct TokenCreateInputJson {
        #[serde(rename = "issuerPublicKey")]
        issuer_public_key: String,
        #[serde(rename = "tokenName")]
        token_name: String,
        #[serde(rename = "tokenTicker")]
        token_ticker: String,
        decimals: u32,
        #[serde(rename = "maxSupply")]
        max_supply: String,
        #[serde(rename = "isFreezable")]
        is_freezable: bool,
        #[serde(rename = "creationEntityPublicKey")]
        creation_entity_public_key: Option<String>,
        #[serde(rename = "extraMetadata")]
        extra_metadata: Option<String>,
    }

    #[derive(Debug, Deserialize)]
    struct PartialTokenOutputJson {
        #[serde(rename = "ownerPublicKey")]
        owner_public_key: String,
        #[serde(rename = "withdrawBondSats")]
        withdraw_bond_sats: Option<String>,
        #[serde(rename = "withdrawRelativeBlockLocktime")]
        withdraw_relative_block_locktime: Option<String>,
        #[serde(rename = "tokenIdentifier")]
        token_identifier: String,
        #[serde(rename = "tokenAmount")]
        token_amount: String,
    }

    fn decode_base64(value: &str) -> Vec<u8> {
        STANDARD.decode(value).unwrap()
    }

    fn parse_u64_string(value: Option<&str>) -> u64 {
        value
            .map(|value| value.parse().unwrap())
            .unwrap_or_default()
    }

    fn parse_timestamp(value: &str) -> Timestamp {
        let timestamp = OffsetDateTime::parse(value, &Rfc3339)
            .unwrap_or_else(|err| panic!("invalid RFC3339 timestamp {value}: {err}"));
        Timestamp {
            seconds: timestamp.unix_timestamp(),
            nanos: timestamp.nanosecond() as i32,
        }
    }

    fn parse_network(value: &str) -> i32 {
        match value {
            "REGTEST" => Network::Regtest as i32,
            "TESTNET" => Network::Testnet as i32,
            "SIGNET" => Network::Signet as i32,
            "MAINNET" => Network::Mainnet as i32,
            other => panic!("unsupported network {other}"),
        }
    }

    fn build_partial_transaction(json: PartialTokenTransactionJson) -> PartialTokenTransaction {
        let token_inputs = match (json.mint_input, json.transfer_input, json.create_input) {
            (Some(mint_input), None, None) => Some(
                partial_token_transaction::TokenInputs::MintInput(spark_token::TokenMintInput {
                    issuer_public_key: decode_base64(&mint_input.issuer_public_key),
                    token_identifier: mint_input
                        .token_identifier
                        .map(|token_identifier| decode_base64(&token_identifier)),
                }),
            ),
            (None, Some(transfer_input), None) => Some(
                partial_token_transaction::TokenInputs::TransferInput(TokenTransferInput {
                    outputs_to_spend: transfer_input
                        .outputs_to_spend
                        .into_iter()
                        .map(|output| TokenOutputToSpend {
                            prev_token_transaction_hash: decode_base64(
                                &output.prev_token_transaction_hash,
                            ),
                            prev_token_transaction_vout: output.prev_token_transaction_vout,
                        })
                        .collect(),
                }),
            ),
            (None, None, Some(create_input)) => {
                Some(partial_token_transaction::TokenInputs::CreateInput(
                    spark_token::TokenCreateInput {
                        issuer_public_key: decode_base64(&create_input.issuer_public_key),
                        token_name: create_input.token_name,
                        token_ticker: create_input.token_ticker,
                        decimals: create_input.decimals,
                        max_supply: decode_base64(&create_input.max_supply),
                        is_freezable: create_input.is_freezable,
                        creation_entity_public_key: create_input
                            .creation_entity_public_key
                            .map(|public_key| decode_base64(&public_key)),
                        extra_metadata: create_input
                            .extra_metadata
                            .map(|extra_metadata| decode_base64(&extra_metadata)),
                    },
                ))
            }
            _ => panic!("expected exactly one token input variant"),
        };

        PartialTokenTransaction {
            version: json.version,
            token_transaction_metadata: json.token_transaction_metadata.map(|metadata| {
                TokenTransactionMetadata {
                    spark_operator_identity_public_keys: metadata
                        .spark_operator_identity_public_keys
                        .into_iter()
                        .map(|key| decode_base64(&key))
                        .collect(),
                    network: parse_network(&metadata.network),
                    client_created_timestamp: metadata
                        .client_created_timestamp
                        .as_deref()
                        .map(parse_timestamp),
                    validity_duration_seconds: parse_u64_string(
                        metadata.validity_duration_seconds.as_deref(),
                    ),
                    invoice_attachments: metadata
                        .invoice_attachments
                        .into_iter()
                        .map(|invoice| spark_token::InvoiceAttachment {
                            spark_invoice: invoice.spark_invoice,
                        })
                        .collect(),
                }
            }),
            token_inputs,
            partial_token_outputs: json
                .partial_token_outputs
                .into_iter()
                .map(|output| PartialTokenOutput {
                    owner_public_key: decode_base64(&output.owner_public_key),
                    withdraw_bond_sats: parse_u64_string(output.withdraw_bond_sats.as_deref()),
                    withdraw_relative_block_locktime: parse_u64_string(
                        output.withdraw_relative_block_locktime.as_deref(),
                    ),
                    token_identifier: decode_base64(&output.token_identifier),
                    token_amount: decode_base64(&output.token_amount),
                })
                .collect(),
            execute_before: json.execute_before.as_deref().map(parse_timestamp),
        }
    }

    fn partial_hash_cases_path() -> PathBuf {
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../spark/testdata/partial_token_transaction_hash_cases.json")
    }

    #[test]
    fn hash_partial_token_transaction_matches_shared_hash_cases() {
        let data = fs::read_to_string(partial_hash_cases_path()).unwrap();
        let file: PartialHashCaseFile = serde_json::from_str(&data).unwrap();

        for tc in file.test_cases {
            let derived_partial_transaction =
                build_partial_transaction(tc.partial_token_transaction);
            let derived_encoded_bytes = derived_partial_transaction.encode_to_vec();

            if tc.expected_hash.is_empty() {
                let computed_hash =
                    hash_partial_token_transaction_impl(&derived_encoded_bytes).unwrap();
                println!(
                    "COMPUTED_PARTIAL_CASE {}: hash={}",
                    tc.name,
                    hex_string(&computed_hash),
                );
                continue;
            }

            let hash = hash_partial_token_transaction_impl(&derived_encoded_bytes).unwrap();
            let got_hex = hex_string(&hash);

            assert_eq!(
                tc.expected_hash.to_ascii_lowercase(),
                got_hex,
                "hash mismatch for {}",
                tc.name
            );
        }
    }

    #[test]
    fn build_broadcast_transaction_request_round_trips() {
        let partial = PartialTokenTransaction {
            version: 3,
            token_transaction_metadata: Some(TokenTransactionMetadata {
                spark_operator_identity_public_keys: vec![sample_key(0x03)],
                network: Network::Regtest as i32,
                client_created_timestamp: Some(Timestamp {
                    seconds: 0,
                    nanos: 100_000_000,
                }),
                validity_duration_seconds: 60,
                invoice_attachments: Vec::new(),
            }),
            token_inputs: Some(partial_token_transaction::TokenInputs::TransferInput(
                TokenTransferInput {
                    outputs_to_spend: vec![TokenOutputToSpend {
                        prev_token_transaction_hash: sample_hash(0xaa),
                        prev_token_transaction_vout: 0,
                    }],
                },
            )),
            partial_token_outputs: vec![PartialTokenOutput {
                owner_public_key: sample_key(0x02),
                withdraw_bond_sats: 10_000,
                withdraw_relative_block_locktime: 100,
                token_identifier: sample_token(0x10),
                token_amount: encode_u128_be(50),
            }],
            execute_before: None,
        };

        let encoded = build_broadcast_transaction_request_impl(BroadcastBuildRequest {
            identity_public_key: sample_key(0x01),
            partial_token_transaction_bytes: partial.encode_to_vec(),
            owner_signatures: vec![SignatureWithIndexInput {
                input_index: 0,
                public_key: sample_key(0x05),
                signature: vec![0x30; 64],
            }],
        })
        .unwrap();

        let decoded = BroadcastTransactionRequest::decode(encoded.as_slice()).unwrap();
        assert_eq!(decoded.identity_public_key, sample_key(0x01));
        assert_eq!(decoded.token_transaction_owner_signatures.len(), 1);
        assert!(decoded.partial_token_transaction.is_some());
    }

    #[test]
    fn build_broadcast_transaction_request_rejects_missing_transfer_signature() {
        let partial = PartialTokenTransaction {
            version: 3,
            token_transaction_metadata: Some(TokenTransactionMetadata::default()),
            token_inputs: Some(partial_token_transaction::TokenInputs::TransferInput(
                TokenTransferInput {
                    outputs_to_spend: vec![
                        TokenOutputToSpend {
                            prev_token_transaction_hash: sample_hash(0xaa),
                            prev_token_transaction_vout: 0,
                        },
                        TokenOutputToSpend {
                            prev_token_transaction_hash: sample_hash(0xbb),
                            prev_token_transaction_vout: 1,
                        },
                    ],
                },
            )),
            partial_token_outputs: vec![],
            execute_before: None,
        };

        let error = build_broadcast_transaction_request_impl(BroadcastBuildRequest {
            identity_public_key: sample_key(0x01),
            partial_token_transaction_bytes: partial.encode_to_vec(),
            owner_signatures: vec![SignatureWithIndexInput {
                input_index: 0,
                public_key: sample_key(0x05),
                signature: vec![0x30; 64],
            }],
        })
        .unwrap_err();

        assert!(error
            .to_string()
            .contains("requires exactly 2 owner signatures"));
    }

    #[test]
    fn build_broadcast_transaction_request_rejects_duplicate_transfer_signature_index() {
        let partial = PartialTokenTransaction {
            version: 3,
            token_transaction_metadata: Some(TokenTransactionMetadata::default()),
            token_inputs: Some(partial_token_transaction::TokenInputs::TransferInput(
                TokenTransferInput {
                    outputs_to_spend: vec![
                        TokenOutputToSpend {
                            prev_token_transaction_hash: sample_hash(0xaa),
                            prev_token_transaction_vout: 0,
                        },
                        TokenOutputToSpend {
                            prev_token_transaction_hash: sample_hash(0xbb),
                            prev_token_transaction_vout: 1,
                        },
                    ],
                },
            )),
            partial_token_outputs: vec![],
            execute_before: None,
        };

        let error = build_broadcast_transaction_request_impl(BroadcastBuildRequest {
            identity_public_key: sample_key(0x01),
            partial_token_transaction_bytes: partial.encode_to_vec(),
            owner_signatures: vec![
                SignatureWithIndexInput {
                    input_index: 0,
                    public_key: sample_key(0x05),
                    signature: vec![0x30; 64],
                },
                SignatureWithIndexInput {
                    input_index: 0,
                    public_key: sample_key(0x06),
                    signature: vec![0x31; 64],
                },
            ],
        })
        .unwrap_err();

        assert!(error.to_string().contains("duplicate owner signature"));
    }

    #[test]
    fn build_broadcast_transaction_request_rejects_non_zero_create_signature_index() {
        let partial = PartialTokenTransaction {
            version: 3,
            token_transaction_metadata: Some(TokenTransactionMetadata::default()),
            token_inputs: Some(partial_token_transaction::TokenInputs::CreateInput(
                spark_token::TokenCreateInput {
                    issuer_public_key: sample_key(0x09),
                    token_name: "TEST".to_owned(),
                    token_ticker: "TST".to_owned(),
                    decimals: 6,
                    max_supply: encode_u128_be(1_000),
                    is_freezable: false,
                    creation_entity_public_key: None,
                    extra_metadata: None,
                },
            )),
            partial_token_outputs: vec![],
            execute_before: None,
        };

        let error = build_broadcast_transaction_request_impl(BroadcastBuildRequest {
            identity_public_key: sample_key(0x01),
            partial_token_transaction_bytes: partial.encode_to_vec(),
            owner_signatures: vec![SignatureWithIndexInput {
                input_index: 1,
                public_key: sample_key(0x05),
                signature: vec![0x30; 64],
            }],
        })
        .unwrap_err();

        assert!(error.to_string().contains("input_index 0"));
    }
}
