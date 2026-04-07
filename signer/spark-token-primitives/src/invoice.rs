use bech32::{Bech32m, Hrp};
use prost::Message;
use prost_types::Timestamp;
use sha2::{Digest, Sha256};
use uuid::Uuid;

use crate::{
    proto::spark::{self, Network, SparkAddress, SparkInvoiceFields, TokensPayment},
    FinalizeTokenInvoiceRequest, PrepareTokenInvoiceRequest, PreparedTokenInvoice,
    SparkTokenPrimitivesError,
};

pub(crate) fn prepare_token_invoice_impl(
    request: PrepareTokenInvoiceRequest,
) -> Result<PreparedTokenInvoice, SparkTokenPrimitivesError> {
    validate_prepare_request(&request)?;

    let sender_public_key = if let Some(sender_spark_address) = &request.sender_spark_address {
        Some(decode_spark_address(sender_spark_address, request.network)?.identity_public_key)
    } else {
        None
    };

    let spark_invoice_fields = SparkInvoiceFields {
        version: 1,
        id: request
            .invoice_id
            .clone()
            .unwrap_or_else(|| Uuid::now_v7().into_bytes().to_vec()),
        payment_type: Some(spark::spark_invoice_fields::PaymentType::TokensPayment(
            TokensPayment {
                token_identifier: request.token_identifier.clone(),
                amount: request.token_amount.clone(),
            },
        )),
        memo: request.memo.clone(),
        sender_public_key: sender_public_key.clone(),
        expiry_time: request
            .expiry_time_unix_millis
            .map(timestamp_from_unix_millis),
    };

    validate_spark_invoice_fields(&spark_invoice_fields)?;

    let spark_invoice_hash = hash_spark_invoice(
        &spark_invoice_fields,
        &request.receiver_identity_public_key,
        request.network,
    )?;
    let spark_invoice_fields_bytes =
        encode_spark_invoice_fields_v1_canonical(&spark_invoice_fields)?;
    let unsigned_spark_address = encode_spark_address(
        &request.receiver_identity_public_key,
        request.network,
        Some(&spark_invoice_fields_bytes),
        None,
    )?;

    Ok(PreparedTokenInvoice {
        spark_invoice_fields_bytes,
        spark_invoice_hash,
        unsigned_spark_address,
    })
}

pub(crate) fn finalize_token_invoice_impl(
    request: FinalizeTokenInvoiceRequest,
) -> Result<String, SparkTokenPrimitivesError> {
    validate_finalize_request(&request)?;

    let spark_invoice_fields = SparkInvoiceFields::decode(
        request.spark_invoice_fields_bytes.as_slice(),
    )
    .map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!(
            "failed to decode spark_invoice_fields_bytes: {err}"
        ))
    })?;
    validate_spark_invoice_fields(&spark_invoice_fields)?;

    encode_spark_address(
        &request.receiver_identity_public_key,
        request.network,
        Some(&request.spark_invoice_fields_bytes),
        request.signature.as_deref(),
    )
}

fn validate_prepare_request(
    request: &PrepareTokenInvoiceRequest,
) -> Result<(), SparkTokenPrimitivesError> {
    validate_length(
        &request.receiver_identity_public_key,
        33,
        "receiver_identity_public_key",
    )?;
    validate_network(request.network)?;

    if let Some(token_identifier) = &request.token_identifier {
        validate_length(token_identifier, 32, "token_identifier")?;
    }
    if let Some(token_amount) = &request.token_amount {
        if token_amount.len() > 16 {
            return Err(format!(
                "token_amount must be at most 16 bytes, got {}",
                token_amount.len()
            )
            .into());
        }
    }
    if let Some(invoice_id) = &request.invoice_id {
        validate_length(invoice_id, 16, "invoice_id")?;
    }

    if let Some(memo) = &request.memo {
        if memo.len() > 120 {
            return Err("memo must be at most 120 bytes".into());
        }
    }

    Ok(())
}

fn validate_finalize_request(
    request: &FinalizeTokenInvoiceRequest,
) -> Result<(), SparkTokenPrimitivesError> {
    validate_length(
        &request.receiver_identity_public_key,
        33,
        "receiver_identity_public_key",
    )?;
    validate_network(request.network)?;
    if request.spark_invoice_fields_bytes.is_empty() {
        return Err("spark_invoice_fields_bytes must not be empty".into());
    }
    if let Some(signature) = &request.signature {
        validate_length(signature, 64, "signature")?;
    }
    Ok(())
}

fn validate_spark_invoice_fields(
    fields: &SparkInvoiceFields,
) -> Result<(), SparkTokenPrimitivesError> {
    if fields.version != 1 {
        return Err("version must be 1".into());
    }
    validate_length(&fields.id, 16, "spark_invoice_fields.id")?;

    if let Some(memo) = &fields.memo {
        if memo.len() > 120 {
            return Err("memo must be at most 120 bytes".into());
        }
    }

    if let Some(sender_public_key) = &fields.sender_public_key {
        validate_length(
            sender_public_key,
            33,
            "spark_invoice_fields.sender_public_key",
        )?;
    }

    let payment_type = fields
        .payment_type
        .as_ref()
        .ok_or_else(|| SparkTokenPrimitivesError::Spark("payment_type is required".to_owned()))?;

    match payment_type {
        spark::spark_invoice_fields::PaymentType::TokensPayment(tokens_payment) => {
            if let Some(token_identifier) = &tokens_payment.token_identifier {
                validate_length(
                    token_identifier,
                    32,
                    "spark_invoice_fields.tokens_payment.token_identifier",
                )?;
            }
            if let Some(token_amount) = &tokens_payment.amount {
                if token_amount.len() > 16 {
                    return Err(
                        "spark_invoice_fields.tokens_payment.amount must be at most 16 bytes"
                            .into(),
                    );
                }
            }
        }
        spark::spark_invoice_fields::PaymentType::SatsPayment(_) => {
            return Err("expected token invoice, got sats invoice".into())
        }
    }

    Ok(())
}

fn decode_spark_address(
    address: &str,
    expected_network: u32,
) -> Result<SparkAddress, SparkTokenPrimitivesError> {
    let (hrp, data) = bech32::decode(address).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("failed to decode spark address: {err}"))
    })?;
    let expected_hrp = network_to_primary_hrp(expected_network)?;
    let hrp_str = hrp.as_str().to_ascii_lowercase();
    if !matches_network_hrp(&hrp_str, expected_network) {
        return Err(format!(
            "spark address network mismatch: expected {expected_hrp}, got {}",
            hrp.as_str()
        )
        .into());
    }

    let spark_address = SparkAddress::decode(data.as_slice()).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("failed to decode SparkAddress: {err}"))
    })?;
    validate_length(
        &spark_address.identity_public_key,
        33,
        "spark_address.identity_public_key",
    )?;
    Ok(spark_address)
}

fn encode_spark_address(
    receiver_identity_public_key: &[u8],
    network: u32,
    spark_invoice_fields_bytes: Option<&[u8]>,
    signature: Option<&[u8]>,
) -> Result<String, SparkTokenPrimitivesError> {
    validate_length(
        receiver_identity_public_key,
        33,
        "receiver_identity_public_key",
    )?;
    let hrp = network_to_primary_hrp(network)?;

    let mut out = Vec::new();
    encode_bytes_field(1, receiver_identity_public_key, &mut out);
    if let Some(spark_invoice_fields_bytes) = spark_invoice_fields_bytes {
        encode_bytes_field(2, spark_invoice_fields_bytes, &mut out);
    }
    if let Some(signature) = signature {
        validate_length(signature, 64, "signature")?;
        encode_bytes_field(3, signature, &mut out);
    }

    bech32::encode::<Bech32m>(hrp, &out).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("failed to encode spark address: {err}"))
    })
}

fn encode_spark_invoice_fields_v1_canonical(
    fields: &SparkInvoiceFields,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let mut out = Vec::new();

    if fields.version != 0 {
        encode_uint32_field(1, fields.version, &mut out);
    }
    if !fields.id.is_empty() {
        encode_bytes_field(2, &fields.id, &mut out);
    }
    if let Some(memo) = &fields.memo {
        encode_string_field(5, memo, &mut out);
    }
    if let Some(sender_public_key) = &fields.sender_public_key {
        encode_bytes_field(6, sender_public_key, &mut out);
    }
    if let Some(expiry_time) = &fields.expiry_time {
        let timestamp_bytes = encode_timestamp(expiry_time);
        encode_bytes_field(7, &timestamp_bytes, &mut out);
    }

    match fields.payment_type.as_ref() {
        Some(spark::spark_invoice_fields::PaymentType::TokensPayment(tokens_payment)) => {
            encode_bytes_field(3, &tokens_payment.encode_to_vec(), &mut out);
        }
        Some(spark::spark_invoice_fields::PaymentType::SatsPayment(_)) => {
            return Err("expected token invoice, got sats invoice".into())
        }
        None => return Err("payment_type is required".into()),
    }

    Ok(out)
}

fn hash_spark_invoice(
    fields: &SparkInvoiceFields,
    receiver_public_key: &[u8],
    network: u32,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    validate_length(receiver_public_key, 33, "receiver_public_key")?;
    validate_spark_invoice_fields(fields)?;

    let mut all_hashes = vec![
        sha256(&u32::to_be_bytes(fields.version)),
        sha256(&fields.id),
        hash_network_magic(network)?,
        sha256(receiver_public_key),
    ];

    let payment_type = fields
        .payment_type
        .as_ref()
        .ok_or_else(|| SparkTokenPrimitivesError::Spark("payment_type is required".to_owned()))?;
    match payment_type {
        spark::spark_invoice_fields::PaymentType::TokensPayment(tokens_payment) => {
            all_hashes.push(sha256(&[1]));
            let token_identifier = tokens_payment
                .token_identifier
                .as_deref()
                .filter(|value| !value.is_empty())
                .unwrap_or(&[0_u8; 32]);
            if token_identifier.len() != 32 {
                return Err("token identifier must be exactly 32 bytes".into());
            }
            all_hashes.push(sha256(token_identifier));
            all_hashes.push(sha256(tokens_payment.amount.as_deref().unwrap_or(&[])));
        }
        spark::spark_invoice_fields::PaymentType::SatsPayment(_) => {
            return Err("expected token invoice, got sats invoice".into())
        }
    }

    all_hashes.push(sha256(
        fields.memo.as_deref().unwrap_or_default().as_bytes(),
    ));

    let sender_public_key = fields
        .sender_public_key
        .as_deref()
        .filter(|value| !value.is_empty())
        .unwrap_or(&[0_u8; 33]);
    if sender_public_key.len() != 33 {
        return Err("sender public key must be exactly 33 bytes".into());
    }
    all_hashes.push(sha256(sender_public_key));

    let expiry_seconds = fields
        .expiry_time
        .as_ref()
        .map(|timestamp| timestamp.seconds.max(0) as u64)
        .unwrap_or(0);
    all_hashes.push(sha256(&expiry_seconds.to_be_bytes()));

    let mut final_hasher = Sha256::new();
    for hash in all_hashes {
        final_hasher.update(hash);
    }
    Ok(final_hasher.finalize().to_vec())
}

fn hash_network_magic(network: u32) -> Result<[u8; 32], SparkTokenPrimitivesError> {
    let magic = match network {
        x if x == Network::Mainnet as u32 => 0xd9b4bef9_u32,
        x if x == Network::Regtest as u32 => 0xdab5bffa_u32,
        x if x == Network::Testnet as u32 => 0x0709_110b_u32,
        x if x == Network::Signet as u32 => 0x40cf_030a_u32,
        _ => return Err(format!("invalid spark network value: {network}").into()),
    };
    Ok(sha256(&sha256(&magic.to_be_bytes())))
}

fn timestamp_from_unix_millis(unix_millis: u64) -> Timestamp {
    Timestamp {
        seconds: (unix_millis / 1000) as i64,
        nanos: ((unix_millis % 1000) * 1_000_000) as i32,
    }
}

fn encode_timestamp(timestamp: &Timestamp) -> Vec<u8> {
    let mut out = Vec::new();
    if timestamp.seconds != 0 {
        encode_int64_field(1, timestamp.seconds, &mut out);
    }
    if timestamp.nanos != 0 {
        encode_int32_field(2, timestamp.nanos, &mut out);
    }
    out
}

fn validate_network(network: u32) -> Result<(), SparkTokenPrimitivesError> {
    if Network::try_from(network as i32).is_err() || network == Network::Unspecified as u32 {
        return Err(format!("invalid spark network value: {network}").into());
    }
    Ok(())
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

fn encode_uint32_field(field_number: u32, value: u32, out: &mut Vec<u8>) {
    encode_key(field_number, 0, out);
    encode_varint(value as u64, out);
}

fn encode_int32_field(field_number: u32, value: i32, out: &mut Vec<u8>) {
    encode_key(field_number, 0, out);
    encode_varint(value as u64, out);
}

fn encode_int64_field(field_number: u32, value: i64, out: &mut Vec<u8>) {
    encode_key(field_number, 0, out);
    encode_varint(value as u64, out);
}

fn encode_bytes_field(field_number: u32, value: &[u8], out: &mut Vec<u8>) {
    encode_key(field_number, 2, out);
    encode_varint(value.len() as u64, out);
    out.extend_from_slice(value);
}

fn encode_string_field(field_number: u32, value: &str, out: &mut Vec<u8>) {
    encode_bytes_field(field_number, value.as_bytes(), out);
}

fn encode_key(field_number: u32, wire_type: u8, out: &mut Vec<u8>) {
    encode_varint(((field_number as u64) << 3) | wire_type as u64, out);
}

fn encode_varint(mut value: u64, out: &mut Vec<u8>) {
    while value >= 0x80 {
        out.push((value as u8) | 0x80);
        value >>= 7;
    }
    out.push(value as u8);
}

fn sha256(bytes: &[u8]) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    hasher.finalize().into()
}

#[cfg(test)]
mod tests;
