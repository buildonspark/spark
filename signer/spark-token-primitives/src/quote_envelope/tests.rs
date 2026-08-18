//! A receive flow passes whether or not a signature made under the wrong role still
//! verifies, so the layout can only be held by asserting the shared fixture directly.

use std::fs;
use std::path::PathBuf;

use serde::Deserialize;

use std::collections::BTreeMap;

use super::{
    quote_envelope_digest_impl, receive_attestor_target_impl, QUOTE_REASON_COOP_EXIT,
    QUOTE_REASON_RECEIVE, QUOTE_REASON_SEND, QUOTE_REASON_STATIC_DEPOSIT, QUOTE_ROLE_ATTESTOR,
    QUOTE_ROLE_ISSUER,
};

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct CaseFile {
    reasons: BTreeMap<String, u64>,
    roles: BTreeMap<String, u64>,
    test_cases: Vec<Case>,
    target_cases: Vec<TargetCase>,
    distinct_digest_pairs: Vec<DistinctPair>,
    invalid_cases: Vec<InvalidCase>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct TargetCase {
    name: String,
    call: String,
    ports: Vec<String>,
    #[serde(default)]
    payment_hash: Option<String>,
    #[serde(default)]
    withdrawal_address: Option<String>,
    #[serde(default)]
    leaf_set_hash: Option<String>,
    #[serde(default)]
    txid: Option<String>,
    #[serde(default)]
    vout: Option<u32>,
    expected_digest: String,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Case {
    name: String,
    network: u64,
    manifest_hash: String,
    reason: u64,
    role: u64,
    #[serde(default)]
    payment_hash: Option<String>,
    target: String,
    expected_digest: String,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct DistinctPair {
    name: String,
    a: String,
    b: String,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct InvalidCase {
    name: String,
    call: String,
    #[serde(default)]
    network: u64,
    #[serde(default)]
    manifest_hash: String,
    #[serde(default)]
    reason: u64,
    #[serde(default)]
    role: u64,
    #[serde(default)]
    payment_hash: String,
    #[serde(default)]
    target: String,
    expected_error: String,
}

fn cases_path() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../spark/testdata/quote_envelope_cases.json")
}

fn load_cases() -> CaseFile {
    let data = fs::read_to_string(cases_path()).unwrap();
    serde_json::from_str(&data).unwrap()
}

fn decode_hex(value: &str) -> Vec<u8> {
    assert!(value.len().is_multiple_of(2), "odd-length hex: {value}");
    (0..value.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&value[i..i + 2], 16).expect("valid hex"))
        .collect()
}

fn hex_string(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn target_for(case: &Case) -> Vec<u8> {
    match &case.payment_hash {
        Some(payment_hash) => receive_attestor_target_impl(&decode_hex(payment_hash))
            .unwrap_or_else(|err| panic!("{}: {err}", case.name)),
        None => decode_hex(&case.target),
    }
}

fn public_target_for(case: &Case) -> Option<Vec<u8>> {
    let payment_hash = case.payment_hash.as_ref()?;
    Some(
        crate::receive_attestor_target(decode_hex(payment_hash))
            .unwrap_or_else(|err| panic!("{}: {err}", case.name)),
    )
}

fn public_digest_for(case: &Case) -> Vec<u8> {
    crate::quote_envelope_digest(
        u32::try_from(case.network).unwrap(),
        decode_hex(&case.manifest_hash),
        u32::try_from(case.reason).unwrap(),
        u32::try_from(case.role).unwrap(),
        target_for(case),
    )
    .unwrap_or_else(|err| panic!("{}: {err}", case.name))
}

fn digest_for(case: &Case) -> Vec<u8> {
    quote_envelope_digest_impl(
        case.network,
        &decode_hex(&case.manifest_hash),
        case.reason,
        case.role,
        &target_for(case),
    )
    .unwrap_or_else(|err| panic!("{}: {err}", case.name))
}

#[test]
fn enum_numbering_matches_shared_cases() {
    let file = load_cases();

    let expected_reasons = BTreeMap::from([
        ("RECEIVE".to_string(), QUOTE_REASON_RECEIVE),
        ("SEND".to_string(), QUOTE_REASON_SEND),
        ("COOP_EXIT".to_string(), QUOTE_REASON_COOP_EXIT),
        ("STATIC_DEPOSIT".to_string(), QUOTE_REASON_STATIC_DEPOSIT),
    ]);
    assert_eq!(expected_reasons, file.reasons);

    let expected_roles = BTreeMap::from([
        ("ISSUER".to_string(), QUOTE_ROLE_ISSUER),
        ("ATTESTOR".to_string(), QUOTE_ROLE_ATTESTOR),
    ]);
    assert_eq!(expected_roles, file.roles);
}

#[test]
fn quote_envelope_digest_matches_shared_cases() {
    let file = load_cases();
    assert!(!file.test_cases.is_empty());

    for case in &file.test_cases {
        // Pins the target tag too, not only the outer envelope.
        assert_eq!(
            case.target.to_ascii_lowercase(),
            hex_string(&target_for(case)),
            "target mismatch for {}",
            case.name
        );
        assert_eq!(
            case.expected_digest.to_ascii_lowercase(),
            hex_string(&digest_for(case)),
            "digest mismatch for {}",
            case.name
        );
        assert_eq!(
            case.expected_digest.to_ascii_lowercase(),
            hex_string(&public_digest_for(case)),
            "public digest wrapper disagrees for {}",
            case.name
        );
        if let Some(public_target) = public_target_for(case) {
            assert_eq!(
                case.target.to_ascii_lowercase(),
                hex_string(&public_target),
                "public target wrapper disagrees for {}",
                case.name
            );
        }
    }
}

#[test]
fn enum_components_separate_domains() {
    let file = load_cases();
    assert!(!file.distinct_digest_pairs.is_empty());

    let named = |name: &str| -> Vec<u8> {
        let case = file
            .test_cases
            .iter()
            .find(|case| case.name == name)
            .unwrap_or_else(|| panic!("distinctDigestPairs names a missing case {name}"));
        digest_for(case)
    };

    for pair in &file.distinct_digest_pairs {
        assert_ne!(
            hex_string(&named(&pair.a)),
            hex_string(&named(&pair.b)),
            "{} and {} hash identically ({})",
            pair.a,
            pair.b,
            pair.name
        );
    }
}

#[test]
fn rejects_shared_invalid_cases() {
    let file = load_cases();
    assert!(!file.invalid_cases.is_empty());

    for case in &file.invalid_cases {
        let err = match case.call.as_str() {
            "receiveAttestationTarget" => {
                receive_attestor_target_impl(&decode_hex(&case.payment_hash))
                    .expect_err(&format!("{}: expected rejection", case.name))
            }
            "quoteEnvelopeDigest" => quote_envelope_digest_impl(
                case.network,
                &decode_hex(&case.manifest_hash),
                case.reason,
                case.role,
                &decode_hex(&case.target),
            )
            .expect_err(&format!("{}: expected rejection", case.name)),
            other => panic!("{}: unknown call {other}", case.name),
        };

        assert!(
            !case.expected_error.is_empty(),
            "{}: no expectedError",
            case.name
        );
        let message = err.to_string();
        assert!(
            message.contains(&case.expected_error),
            "{}: expected={:?} got={message:?}",
            case.name,
            case.expected_error
        );

        let public_err = match case.call.as_str() {
            "receiveAttestationTarget" => {
                Some(crate::receive_attestor_target(decode_hex(&case.payment_hash)).unwrap_err())
            }
            _ => u32::try_from(case.network).ok().map(|network| {
                crate::quote_envelope_digest(
                    network,
                    decode_hex(&case.manifest_hash),
                    u32::try_from(case.reason).unwrap(),
                    u32::try_from(case.role).unwrap(),
                    decode_hex(&case.target),
                )
                .unwrap_err()
            }),
        };
        if let Some(err) = public_err {
            assert!(
                err.to_string().contains(&case.expected_error),
                "{}: public wrapper expected={:?} got={:?}",
                case.name,
                case.expected_error,
                err.to_string()
            );
        }
    }
}

/// Every target derivation the core implements, including the two that a hand-port loses: bech32
/// case-folding, and a `vout` bounded to uint32 but committed as a uint64. The three non-receive
/// expectations were cross-checked against sparkcore's own pinned vectors before landing here.
#[test]
fn target_derivations_match_shared_cases() {
    let file = load_cases();
    assert!(
        !file.target_cases.is_empty(),
        "fixture carries no target cases"
    );

    let mut by_name = BTreeMap::new();
    for case in &file.target_cases {
        assert!(
            case.ports.iter().any(|port| port == "rust"),
            "{}: the core implements every target, so every case must list rust",
            case.name
        );
        let digest = match case.call.as_str() {
            "receiveAttestorTarget" => super::receive_attestor_target_impl(&decode_hex(
                case.payment_hash.as_ref().expect("payment_hash"),
            )),
            "sendTarget" => super::send_target_impl(&decode_hex(
                case.payment_hash.as_ref().expect("payment_hash"),
            )),
            "coopExitTarget" => super::coop_exit_target_impl(
                case.withdrawal_address
                    .as_ref()
                    .expect("withdrawal_address"),
                &decode_hex(case.leaf_set_hash.as_ref().expect("leaf_set_hash")),
            ),
            "staticDepositTarget" => super::static_deposit_target_impl(
                &decode_hex(case.txid.as_ref().expect("txid")),
                case.vout.expect("vout"),
            ),
            other => panic!("{}: unknown target call {other}", case.name),
        }
        .unwrap_or_else(|err| panic!("{}: {err}", case.name));

        assert_eq!(hex_string(&digest), case.expected_digest, "{}", case.name);
        by_name.insert(case.name.clone(), hex_string(&digest));
    }

    // Bech32 is case-insensitive, so these must not merely both succeed — they must agree.
    if let (Some(lower), Some(upper)) = (
        by_name.get("coop_exit_valid_lowercase"),
        by_name.get("coop_exit_valid_uppercase"),
    ) {
        assert_eq!(
            lower, upper,
            "case-equivalent addresses must hash identically"
        );
    }
    // And a rendering the validator refuses is bound verbatim, so it must NOT collapse onto them.
    if let (Some(lower), Some(mixed)) = (
        by_name.get("coop_exit_valid_lowercase"),
        by_name.get("coop_exit_mixed_case_verbatim"),
    ) {
        assert_ne!(lower, mixed, "a refused rendering must not canonicalize");
    }
}

/// The public wrappers are what sparkcore reaches through the wheel, so they need their own pass.
#[test]
fn public_target_wrappers_match_shared_cases() {
    for case in load_cases().target_cases {
        let digest = match case.call.as_str() {
            "receiveAttestorTarget" => crate::receive_attestor_target(decode_hex(
                case.payment_hash.as_ref().expect("payment_hash"),
            )),
            "sendTarget" => crate::send_target(decode_hex(
                case.payment_hash.as_ref().expect("payment_hash"),
            )),
            "coopExitTarget" => crate::coop_exit_target(
                case.withdrawal_address.clone().expect("withdrawal_address"),
                decode_hex(case.leaf_set_hash.as_ref().expect("leaf_set_hash")),
            ),
            "staticDepositTarget" => crate::static_deposit_target(
                decode_hex(case.txid.as_ref().expect("txid")),
                case.vout.expect("vout"),
            ),
            other => panic!("{}: unknown target call {other}", case.name),
        }
        .unwrap_or_else(|err| panic!("{}: {err}", case.name));
        assert_eq!(hex_string(&digest), case.expected_digest, "{}", case.name);
    }
}
