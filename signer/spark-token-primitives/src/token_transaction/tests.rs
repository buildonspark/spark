use base64::{engine::general_purpose::STANDARD, Engine as _};
use bech32::{Bech32m, Hrp};
use prost::Message;
use prost_types::Timestamp;
use serde::Deserialize;
use std::{fs, path::PathBuf};
use time::{format_description::well_known::Rfc3339, OffsetDateTime};

use super::{
    build_broadcast_transaction_request_impl,
    construct::{decode_u128_be, encode_u128_be},
    construct_partial_transfer_transaction_impl,
    hash::hex_string,
    hash_partial_token_transaction_impl,
};
use crate::{
    proto::{
        spark::{self, Network, SparkAddress, SparkInvoiceFields, TokensPayment},
        spark_token::{
            self, partial_token_transaction, BroadcastTransactionRequest, PartialTokenOutput,
            PartialTokenTransaction, TokenOutputToSpend, TokenTransactionMetadata,
            TokenTransferInput,
        },
    },
    BroadcastBuildRequest, ReceiverTokenOutput, SelectedTokenOutput, SignatureWithIndexInput,
    TransferBuildRequest,
};

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
            receiver_spark_address: encode_spark_address(sample_key(0x02), Network::Regtest, None),
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
        PartialTokenTransaction::decode(result.partial_token_transaction_bytes.as_slice()).unwrap();

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
            receiver_spark_address: encode_spark_address(sample_key(0x02), Network::Regtest, None),
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
            receiver_spark_address: encode_spark_address(sample_key(0x02), Network::Regtest, None),
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
    let token_amount = vec![80];
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
        PartialTokenTransaction::decode(result.partial_token_transaction_bytes.as_slice()).unwrap();

    assert_eq!(
        partial.partial_token_outputs[0].owner_public_key,
        receiver_public_key
    );
    assert_eq!(
        partial.partial_token_outputs[0].token_identifier,
        token_identifier
    );
    assert_eq!(
        partial.partial_token_outputs[0].token_amount,
        encode_u128_be(80)
    );
    let metadata = partial.token_transaction_metadata.unwrap();
    assert_eq!(metadata.invoice_attachments.len(), 1);
    assert_eq!(metadata.invoice_attachments[0].spark_invoice, invoice);
}

#[test]
fn construct_partial_transfer_transaction_accepts_var_bytes_invoice_amount_when_matching_explicit_amount(
) {
    let token_identifier = sample_token(0x10);
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
                    token_identifier: Some(token_identifier.clone()),
                    amount: Some(vec![80]),
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
            receiver_spark_address: invoice,
            token_identifier: Some(token_identifier),
            token_amount: Some(encode_u128_be(80)),
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
        PartialTokenTransaction::decode(result.partial_token_transaction_bytes.as_slice()).unwrap();

    assert_eq!(
        partial.partial_token_outputs[0].token_amount,
        encode_u128_be(80)
    );
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
    let parsed = OffsetDateTime::parse(value, &Rfc3339)
        .unwrap_or_else(|err| panic!("invalid RFC3339 timestamp {value}: {err}"));
    Timestamp {
        seconds: parsed.unix_timestamp(),
        nanos: parsed.nanosecond() as i32,
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
        (Some(mint_input), None, None) => Some(partial_token_transaction::TokenInputs::MintInput(
            spark_token::TokenMintInput {
                issuer_public_key: decode_base64(&mint_input.issuer_public_key),
                token_identifier: mint_input
                    .token_identifier
                    .map(|token_identifier| decode_base64(&token_identifier)),
            },
        )),
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
        (None, None, Some(create_input)) => Some(
            partial_token_transaction::TokenInputs::CreateInput(spark_token::TokenCreateInput {
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
            }),
        ),
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
        let derived_partial_transaction = build_partial_transaction(tc.partial_token_transaction);
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
