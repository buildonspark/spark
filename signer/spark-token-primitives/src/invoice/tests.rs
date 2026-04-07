use super::*;
use std::{fs, path::PathBuf};

use base64::{engine::general_purpose::STANDARD, Engine as _};
use serde::Deserialize;
use time::{format_description::well_known::Rfc3339, OffsetDateTime};

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct TokenInvoiceSigningHashCaseFile {
    test_cases: Vec<TokenInvoiceSigningHashCase>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct TokenInvoiceCanonicalEncodingCaseFile {
    test_cases: Vec<TokenInvoiceCanonicalEncodingCase>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct TokenInvoiceSigningHashCase {
    name: String,
    network: String,
    receiver_public_key: String,
    expected_hash: String,
    spark_invoice_fields: FixtureSparkInvoiceFields,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct TokenInvoiceCanonicalEncodingCase {
    name: String,
    expected_canonical_encoding: String,
    spark_invoice_fields: FixtureSparkInvoiceFields,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct FixtureSparkInvoiceFields {
    version: u32,
    id: String,
    #[serde(default)]
    memo: Option<String>,
    #[serde(default)]
    sender_public_key: Option<String>,
    #[serde(default)]
    expiry_time: Option<String>,
    #[serde(default)]
    tokens_payment: Option<FixtureTokensPayment>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct FixtureTokensPayment {
    #[serde(default)]
    token_identifier: Option<String>,
    #[serde(default)]
    amount: Option<String>,
}

impl FixtureSparkInvoiceFields {
    fn to_proto(&self) -> SparkInvoiceFields {
        SparkInvoiceFields {
            version: self.version,
            id: decode_base64(&self.id),
            payment_type: Some(spark::spark_invoice_fields::PaymentType::TokensPayment(
                TokensPayment {
                    token_identifier: self
                        .tokens_payment
                        .as_ref()
                        .and_then(|payment| payment.token_identifier.as_ref())
                        .map(|value| decode_base64(value)),
                    amount: self
                        .tokens_payment
                        .as_ref()
                        .and_then(|payment| payment.amount.as_ref())
                        .map(|value| decode_base64(value)),
                },
            )),
            memo: self.memo.clone(),
            sender_public_key: self
                .sender_public_key
                .as_ref()
                .map(|value| decode_base64(value)),
            expiry_time: self
                .expiry_time
                .as_ref()
                .map(|value| parse_timestamp(value)),
        }
    }
}

fn valid_key() -> Vec<u8> {
    hex_to_bytes("02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132")
}

fn sender_key() -> Vec<u8> {
    hex_to_bytes("02112233445566778899aabbccddeeff0123456789abcdef0fedcba98765432100")
}

fn sample_invoice_id() -> Vec<u8> {
    (1_u8..=16).collect()
}

fn sample_signature() -> Vec<u8> {
    vec![0x44; 64]
}

fn sample_token_identifier() -> Vec<u8> {
    (1_u8..=32).collect()
}

fn sample_token_amount() -> Vec<u8> {
    vec![0x03, 0xe8]
}

fn plain_spark_address(public_key: Vec<u8>, network: u32) -> String {
    encode_spark_address(&public_key, network, None, None).unwrap()
}

fn decode_address(address: &str, network: u32) -> SparkAddress {
    decode_spark_address(address, network).unwrap()
}

#[test]
fn prepare_token_invoice_generates_unsigned_invoice_and_hash() {
    let request = PrepareTokenInvoiceRequest {
        receiver_identity_public_key: valid_key(),
        network: Network::Regtest as u32,
        token_identifier: Some(sample_token_identifier()),
        token_amount: Some(sample_token_amount()),
        memo: Some("test token invoice".to_owned()),
        sender_spark_address: Some(plain_spark_address(sender_key(), Network::Regtest as u32)),
        expiry_time_unix_millis: Some(1_640_995_200_123),
        invoice_id: Some(sample_invoice_id()),
    };

    let prepared = prepare_token_invoice_impl(request).unwrap();

    assert_eq!(prepared.spark_invoice_hash.len(), 32);

    let fields =
        SparkInvoiceFields::decode(prepared.spark_invoice_fields_bytes.as_slice()).unwrap();
    assert_eq!(fields.version, 1);
    assert_eq!(fields.id, sample_invoice_id());
    assert_eq!(fields.memo.as_deref(), Some("test token invoice"));
    assert_eq!(fields.sender_public_key.as_ref(), Some(&sender_key()));

    let payment = fields.payment_type.unwrap();
    let tokens_payment = match payment {
        spark::spark_invoice_fields::PaymentType::TokensPayment(tokens_payment) => tokens_payment,
        _ => panic!("expected tokens payment"),
    };
    assert_eq!(
        tokens_payment.token_identifier.as_ref(),
        Some(&sample_token_identifier())
    );
    assert_eq!(tokens_payment.amount.as_ref(), Some(&sample_token_amount()));

    let unsigned = decode_address(&prepared.unsigned_spark_address, Network::Regtest as u32);
    assert_eq!(unsigned.identity_public_key, valid_key());
    assert_eq!(unsigned.signature, None);
    assert!(unsigned.spark_invoice_fields.is_some());
}

#[test]
fn prepare_token_invoice_allows_partial_token_fields() {
    let request = PrepareTokenInvoiceRequest {
        receiver_identity_public_key: valid_key(),
        network: Network::Regtest as u32,
        token_identifier: None,
        token_amount: None,
        memo: None,
        sender_spark_address: None,
        expiry_time_unix_millis: None,
        invoice_id: Some(sample_invoice_id()),
    };

    let prepared = prepare_token_invoice_impl(request).unwrap();
    let fields =
        SparkInvoiceFields::decode(prepared.spark_invoice_fields_bytes.as_slice()).unwrap();
    let payment = fields.payment_type.unwrap();
    let tokens_payment = match payment {
        spark::spark_invoice_fields::PaymentType::TokensPayment(tokens_payment) => tokens_payment,
        _ => panic!("expected tokens payment"),
    };
    assert_eq!(tokens_payment.token_identifier, None);
    assert_eq!(tokens_payment.amount, None);
}

#[test]
fn finalize_token_invoice_embeds_signature() {
    let prepared = prepare_token_invoice_impl(PrepareTokenInvoiceRequest {
        receiver_identity_public_key: valid_key(),
        network: Network::Regtest as u32,
        token_identifier: Some(sample_token_identifier()),
        token_amount: Some(sample_token_amount()),
        memo: Some("signed".to_owned()),
        sender_spark_address: None,
        expiry_time_unix_millis: None,
        invoice_id: Some(sample_invoice_id()),
    })
    .unwrap();

    let signed_invoice = finalize_token_invoice_impl(FinalizeTokenInvoiceRequest {
        receiver_identity_public_key: valid_key(),
        network: Network::Regtest as u32,
        spark_invoice_fields_bytes: prepared.spark_invoice_fields_bytes.clone(),
        signature: Some(sample_signature()),
    })
    .unwrap();

    let decoded = decode_address(&signed_invoice, Network::Regtest as u32);
    assert_eq!(decoded.signature.as_ref(), Some(&sample_signature()));
    assert!(decoded.spark_invoice_fields.is_some());
}

#[test]
fn prepare_token_invoice_rejects_sender_network_mismatch() {
    let err = prepare_token_invoice_impl(PrepareTokenInvoiceRequest {
        receiver_identity_public_key: valid_key(),
        network: Network::Regtest as u32,
        token_identifier: Some(sample_token_identifier()),
        token_amount: Some(sample_token_amount()),
        memo: None,
        sender_spark_address: Some(plain_spark_address(sender_key(), Network::Testnet as u32)),
        expiry_time_unix_millis: None,
        invoice_id: Some(sample_invoice_id()),
    })
    .unwrap_err();

    assert!(err.to_string().contains("network mismatch"));
}

#[test]
fn token_invoice_signing_hash_fixture_cases_match_expected_hashes() {
    let fixture_file = load_token_invoice_signing_hash_cases();

    for test_case in fixture_file.test_cases {
        let fields = test_case.spark_invoice_fields.to_proto();
        let receiver_public_key = decode_base64(&test_case.receiver_public_key);
        let network = parse_network(&test_case.network);

        let hash = hash_spark_invoice(&fields, &receiver_public_key, network)
            .unwrap_or_else(|err| panic!("fixture {} failed: {err}", test_case.name));

        assert_eq!(
            bytes_to_hex(&hash),
            test_case.expected_hash,
            "fixture {}",
            test_case.name
        );
    }
}

#[test]
fn token_invoice_canonical_encoding_fixture_cases_match_expected_bytes() {
    let fixture_file = load_token_invoice_canonical_encoding_cases();

    for test_case in fixture_file.test_cases {
        let fields = test_case.spark_invoice_fields.to_proto();
        let encoded = encode_spark_invoice_fields_v1_canonical(&fields)
            .unwrap_or_else(|err| panic!("fixture {} failed: {err}", test_case.name));

        assert_eq!(
            bytes_to_hex(&encoded),
            test_case.expected_canonical_encoding,
            "fixture {}",
            test_case.name
        );
    }
}

fn load_token_invoice_signing_hash_cases() -> TokenInvoiceSigningHashCaseFile {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../spark/testdata/token_invoice_signing_hash_cases.json");
    serde_json::from_str(&fs::read_to_string(path).unwrap()).unwrap()
}

fn load_token_invoice_canonical_encoding_cases() -> TokenInvoiceCanonicalEncodingCaseFile {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../spark/testdata/token_invoice_canonical_encoding_cases.json");
    serde_json::from_str(&fs::read_to_string(path).unwrap()).unwrap()
}

fn parse_network(value: &str) -> u32 {
    match value {
        "MAINNET" => Network::Mainnet as u32,
        "REGTEST" => Network::Regtest as u32,
        "TESTNET" => Network::Testnet as u32,
        "SIGNET" => Network::Signet as u32,
        _ => panic!("unknown network {value}"),
    }
}

fn parse_timestamp(value: &str) -> Timestamp {
    let timestamp = OffsetDateTime::parse(value, &Rfc3339).unwrap();
    Timestamp {
        seconds: timestamp.unix_timestamp(),
        nanos: timestamp.nanosecond() as i32,
    }
}

fn decode_base64(value: &str) -> Vec<u8> {
    STANDARD.decode(value).unwrap()
}

fn bytes_to_hex(value: &[u8]) -> String {
    value.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn hex_to_bytes(value: &str) -> Vec<u8> {
    assert!(value.len().is_multiple_of(2));
    value
        .as_bytes()
        .chunks(2)
        .map(|chunk| {
            let hex = std::str::from_utf8(chunk).unwrap();
            u8::from_str_radix(hex, 16).unwrap()
        })
        .collect()
}
