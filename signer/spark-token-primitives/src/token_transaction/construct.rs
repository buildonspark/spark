use std::collections::BTreeMap;

use bech32::Hrp;
use prost::Message;
use prost_types::Timestamp;

use crate::{
    proto::{
        spark::{self, Network, SparkAddress},
        spark_token::{
            self, partial_token_transaction, PartialTokenOutput, PartialTokenTransaction,
            TokenOutputToSpend, TokenTransactionMetadata, TokenTransferInput,
        },
    },
    PartialTransferBuildResult, ReceiverTokenOutput, SelectedTokenOutput,
    SparkTokenPrimitivesError, TransferBuildRequest,
};

use super::{
    hash::{hash_partial_token_transaction, hex_string},
    validate_length,
};

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
        let token_amount = resolve_invoice_or_explicit_token_amount(
            explicit_token_amount,
            tokens_payment.amount,
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

fn resolve_invoice_or_explicit_token_amount(
    explicit: Option<Vec<u8>>,
    embedded: Option<Vec<u8>>,
    field_name: &str,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    if let Some(ref explicit_bytes) = explicit {
        validate_length(explicit_bytes, 16, field_name)?;
    }
    let normalized_embedded = embedded
        .map(|embedded_bytes| normalize_invoice_token_amount(&embedded_bytes, field_name))
        .transpose()?;

    match (explicit, normalized_embedded) {
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

fn normalize_invoice_token_amount(
    bytes: &[u8],
    field_name: &str,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    if bytes.is_empty() || bytes.len() > 16 {
        return Err(format!(
            "{field_name} must be between 1 and 16 bytes in embedded invoice, got {}",
            bytes.len()
        )
        .into());
    }

    let mut normalized = vec![0_u8; 16 - bytes.len()];
    normalized.extend_from_slice(bytes);
    Ok(normalized)
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

pub(super) fn decode_u128_be(
    bytes: &[u8],
    field_name: &str,
) -> Result<u128, SparkTokenPrimitivesError> {
    validate_length(bytes, 16, field_name)?;
    let mut buf = [0_u8; 16];
    buf.copy_from_slice(bytes);
    Ok(u128::from_be_bytes(buf))
}

pub(super) fn encode_u128_be(value: u128) -> Vec<u8> {
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
