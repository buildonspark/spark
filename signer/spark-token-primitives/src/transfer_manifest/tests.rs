use std::fs;
use std::path::PathBuf;

use base64::engine::general_purpose::STANDARD;
use base64::Engine as _;
use prost::Message;
use prost_types::Timestamp;
use serde::Deserialize;
use time::format_description::well_known::Rfc3339;
use time::OffsetDateTime;

use super::hash_transfer_manifest_impl;
use crate::proto::spark::{
    manifest_amount, FeeComponent, FeeRole, FeeSource, ManifestAmount, ManifestEdge, Network,
    TransferManifest,
};

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ManifestHashCaseFile {
    test_cases: Vec<ManifestHashCase>,
    invalid_cases: Vec<ManifestInvalidCase>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ManifestHashCase {
    name: String,
    expected_hash: String,
    transfer_manifest: ManifestJson,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ManifestInvalidCase {
    name: String,
    transfer_manifest: ManifestJson,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ManifestJson {
    #[serde(default)]
    version: u32,
    #[serde(default)]
    transfer_id: String,
    network: Option<EnumJson>,
    transfer_expiry_time: Option<String>,
    quote_expiry_time: Option<String>,
    #[serde(default)]
    edges: Vec<EdgeJson>,
    #[serde(default)]
    fees: Vec<FeeJson>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct EdgeJson {
    #[serde(default)]
    sender_identity_public_key: String,
    #[serde(default)]
    receiver_identity_public_key: String,
    amount: Option<AmountJson>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct FeeJson {
    source: Option<EnumJson>,
    role: Option<EnumJson>,
    #[serde(default)]
    recipient_identity_public_key: String,
    amount: Option<AmountJson>,
}

#[derive(Debug, Deserialize)]
struct AmountJson {
    sats: Option<StringOrU64>,
    bps: Option<u32>,
}

// protojson serializes uint64 as a JSON string; accept both encodings like
// the token fixture parsers do.
#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum StringOrU64 {
    String(String),
    Number(u64),
}

// protojson enums are names for known values; the fixture's unknown-value
// cases use raw numbers.
#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum EnumJson {
    Name(String),
    Number(i32),
}

fn decode_base64(value: &str) -> Vec<u8> {
    STANDARD.decode(value).unwrap()
}

fn parse_timestamp(value: &str) -> Timestamp {
    let parsed = OffsetDateTime::parse(value, &Rfc3339)
        .unwrap_or_else(|err| panic!("invalid RFC3339 timestamp {value}: {err}"));
    Timestamp {
        seconds: parsed.unix_timestamp(),
        nanos: parsed.nanosecond() as i32,
    }
}

fn parse_u64(value: &StringOrU64) -> u64 {
    match value {
        StringOrU64::String(s) => s.parse().unwrap(),
        StringOrU64::Number(n) => *n,
    }
}

fn parse_network(value: Option<&EnumJson>) -> i32 {
    match value {
        None => Network::Unspecified as i32,
        Some(EnumJson::Number(n)) => *n,
        Some(EnumJson::Name(name)) => match name.as_str() {
            "UNSPECIFIED" => Network::Unspecified as i32,
            "MAINNET" => Network::Mainnet as i32,
            "REGTEST" => Network::Regtest as i32,
            "TESTNET" => Network::Testnet as i32,
            "SIGNET" => Network::Signet as i32,
            other => panic!("unsupported network {other}"),
        },
    }
}

fn parse_fee_source(value: Option<&EnumJson>) -> i32 {
    match value {
        None => FeeSource::Unspecified as i32,
        Some(EnumJson::Number(n)) => *n,
        Some(EnumJson::Name(name)) => match name.as_str() {
            "FEE_SOURCE_UNSPECIFIED" => FeeSource::Unspecified as i32,
            "FEE_SOURCE_PARTNER_MARKUP" => FeeSource::PartnerMarkup as i32,
            "FEE_SOURCE_BASE" => FeeSource::Base as i32,
            "FEE_SOURCE_NETWORK" => FeeSource::Network as i32,
            other => panic!("unsupported fee source {other}"),
        },
    }
}

fn parse_fee_role(value: Option<&EnumJson>) -> i32 {
    match value {
        None => FeeRole::Unspecified as i32,
        Some(EnumJson::Number(n)) => *n,
        Some(EnumJson::Name(name)) => match name.as_str() {
            "FEE_ROLE_UNSPECIFIED" => FeeRole::Unspecified as i32,
            "FEE_ROLE_AFFILIATE" => FeeRole::Affiliate as i32,
            "FEE_ROLE_PARTNER" => FeeRole::Partner as i32,
            "FEE_ROLE_LS" => FeeRole::Ls as i32,
            other => panic!("unsupported fee role {other}"),
        },
    }
}

fn parse_amount(value: Option<&AmountJson>) -> Option<ManifestAmount> {
    let value = value?;
    let amount = match (&value.sats, value.bps) {
        (Some(sats), None) => Some(manifest_amount::Amount::Sats(parse_u64(sats))),
        (None, Some(bps)) => Some(manifest_amount::Amount::Bps(bps)),
        (None, None) => None,
        (Some(_), Some(_)) => panic!("fixture amount sets both oneof arms"),
    };
    Some(ManifestAmount { amount })
}

fn build_manifest(json: &ManifestJson) -> TransferManifest {
    TransferManifest {
        version: json.version,
        transfer_id: json.transfer_id.clone(),
        network: parse_network(json.network.as_ref()),
        transfer_expiry_time: json.transfer_expiry_time.as_deref().map(parse_timestamp),
        quote_expiry_time: json.quote_expiry_time.as_deref().map(parse_timestamp),
        edges: json
            .edges
            .iter()
            .map(|edge| ManifestEdge {
                sender_identity_public_key: decode_base64(&edge.sender_identity_public_key),
                receiver_identity_public_key: decode_base64(&edge.receiver_identity_public_key),
                amount: parse_amount(edge.amount.as_ref()),
            })
            .collect(),
        fees: json
            .fees
            .iter()
            .map(|fee| FeeComponent {
                source: parse_fee_source(fee.source.as_ref()),
                role: parse_fee_role(fee.role.as_ref()),
                amount: parse_amount(fee.amount.as_ref()),
                recipient_identity_public_key: decode_base64(&fee.recipient_identity_public_key),
            })
            .collect(),
    }
}

fn hex_string(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn hash_cases_path() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../spark/testdata/transfer_manifest_hash_cases.json")
}

fn load_cases() -> ManifestHashCaseFile {
    let data = fs::read_to_string(hash_cases_path()).unwrap();
    serde_json::from_str(&data).unwrap()
}

#[test]
fn hash_transfer_manifest_matches_shared_hash_cases() {
    let file = load_cases();
    assert!(!file.test_cases.is_empty());

    for tc in file.test_cases {
        let manifest = build_manifest(&tc.transfer_manifest);
        let hash = hash_transfer_manifest_impl(&manifest.encode_to_vec())
            .unwrap_or_else(|err| panic!("{}: {err}", tc.name));

        assert_eq!(
            tc.expected_hash.to_ascii_lowercase(),
            hex_string(&hash),
            "hash mismatch for {}",
            tc.name
        );
    }
}

#[test]
fn rejects_shared_invalid_cases() {
    let file = load_cases();
    assert!(!file.invalid_cases.is_empty());

    for tc in file.invalid_cases {
        let manifest = build_manifest(&tc.transfer_manifest);
        let err = hash_transfer_manifest_impl(&manifest.encode_to_vec())
            .expect_err(&format!("{}: expected rejection", tc.name));

        assert!(
            err.to_string().contains("transfer manifest not hashable"),
            "{}: unexpected error {err}",
            tc.name
        );
    }
}

#[test]
fn rejects_undecodable_bytes() {
    let err = hash_transfer_manifest_impl(&[0xff, 0xff, 0xff, 0xff]).unwrap_err();
    assert!(
        err.to_string()
            .contains("failed to decode TransferManifest"),
        "unexpected error {err}"
    );
}
