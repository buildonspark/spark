//! The digest every party to a fee quote signs. The Go SO hand-ports this rather than
//! binding it, so `testdata/quote_envelope_cases.json` is what keeps the two byte-identical
//! and any layout change must re-mint it.

use crate::hashstructure::Hasher;
use crate::proto::spark::Network;
use crate::SparkTokenPrimitivesError;

const QUOTE_TAG: [&str; 3] = ["spark", "quote", "v1"];
const RECEIVE_ATTESTOR_TARGET_TAG: [&str; 6] =
    ["spark", "quote", "target", "receive", "attestor", "v1"];
const SEND_TARGET_TAG: [&str; 5] = ["spark", "quote", "target", "send", "v1"];
const COOP_EXIT_TARGET_TAG: [&str; 5] = ["spark", "quote", "target", "coop_exit", "v1"];
const STATIC_DEPOSIT_TARGET_TAG: [&str; 5] = ["spark", "quote", "target", "static_deposit", "v1"];

const DIGEST_LEN: usize = 32;

// Which flow a quote was issued for, so a quote for one flow cannot be replayed into
// another. 1-based so 0 stays an unset sentinel.
const QUOTE_REASON_RECEIVE: u64 = 1;
const QUOTE_REASON_SEND: u64 = 2;
const QUOTE_REASON_COOP_EXIT: u64 = 3;
const QUOTE_REASON_STATIC_DEPOSIT: u64 = 4;

// Bound in even though the two roles already sign different targets: relying on that
// coincidence would break the first time a flow gave both roles the same target.
const QUOTE_ROLE_ISSUER: u64 = 1;
const QUOTE_ROLE_ATTESTOR: u64 = 2;

fn require_32_bytes(name: &str, value: &[u8]) -> Result<(), String> {
    if value.len() != DIGEST_LEN {
        return Err(format!("{name} must be 32 bytes, got {}", value.len()));
    }
    Ok(())
}

fn is_signable_network(network: u64) -> bool {
    match i32::try_from(network)
        .ok()
        .and_then(|value| Network::try_from(value).ok())
    {
        Some(network) => network != Network::Unspecified,
        None => false,
    }
}

fn validate_for_digest(
    network: u64,
    manifest_hash: &[u8],
    reason: u64,
    role: u64,
    target: &[u8],
) -> Result<(), String> {
    require_32_bytes("manifest_hash", manifest_hash)?;
    if !is_signable_network(network) {
        return Err(format!("unsupported network {network}"));
    }
    if !matches!(
        reason,
        QUOTE_REASON_RECEIVE
            | QUOTE_REASON_SEND
            | QUOTE_REASON_COOP_EXIT
            | QUOTE_REASON_STATIC_DEPOSIT
    ) {
        return Err(format!("unsupported quote reason {reason}"));
    }
    if !matches!(role, QUOTE_ROLE_ISSUER | QUOTE_ROLE_ATTESTOR) {
        return Err(format!("unsupported quote role {role}"));
    }
    // An issuer signs before the thing it would bind exists, so its empty target means "nothing
    // else yet". An attestor signs after, so an empty one means a caller dropped the binding.
    if role == QUOTE_ROLE_ATTESTOR && target.is_empty() {
        return Err("attestor target must be non-empty".to_string());
    }
    Ok(())
}

/// The attestor signs at commit time, when the payment hash exists; the issuer signs at
/// quote time, when it does not — so only the attestor's target carries it.
pub(crate) fn receive_attestor_target_impl(
    payment_hash: &[u8],
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    require_32_bytes("payment_hash", payment_hash).map_err(|msg| {
        SparkTokenPrimitivesError::Spark(format!("receive attestor target: {msg}"))
    })?;

    Ok(Hasher::new(&RECEIVE_ATTESTOR_TARGET_TAG)
        .add_bytes(payment_hash)
        .hash())
}

pub(crate) fn send_target_impl(payment_hash: &[u8]) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    require_32_bytes("payment_hash", payment_hash)
        .map_err(|msg| SparkTokenPrimitivesError::Spark(format!("send target: {msg}")))?;

    Ok(Hasher::new(&SEND_TARGET_TAG).add_bytes(payment_hash).hash())
}

/// Bech32 and bech32m are case-insensitive (BIP-173), so equivalent-case renderings of one
/// address must hash identically at quote and at commit. Base58 is case-sensitive, and anything
/// that is not a valid segwit address is bound exactly as given.
fn canonical_withdrawal_address(address: &str) -> String {
    if bech32::segwit::decode(address).is_ok() {
        address.to_lowercase()
    } else {
        address.to_string()
    }
}

pub(crate) fn coop_exit_target_impl(
    withdrawal_address: &str,
    leaf_set_hash: &[u8],
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    if withdrawal_address.is_empty() {
        return Err(SparkTokenPrimitivesError::Spark(
            "coop exit target: withdrawal_address must be non-empty".to_string(),
        ));
    }
    require_32_bytes("leaf_set_hash", leaf_set_hash)
        .map_err(|msg| SparkTokenPrimitivesError::Spark(format!("coop exit target: {msg}")))?;

    Ok(Hasher::new(&COOP_EXIT_TARGET_TAG)
        .add_string(&canonical_withdrawal_address(withdrawal_address))
        .add_bytes(leaf_set_hash)
        .hash())
}

/// `vout` is a `u32` so the range rule is the type, but it is committed as a uint64 — the width
/// in the hash stream is not the width of the bound.
pub(crate) fn static_deposit_target_impl(
    txid: &[u8],
    vout: u32,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    require_32_bytes("txid", txid)
        .map_err(|msg| SparkTokenPrimitivesError::Spark(format!("static deposit target: {msg}")))?;

    Ok(Hasher::new(&STATIC_DEPOSIT_TARGET_TAG)
        .add_bytes(txid)
        .add_uint64(u64::from(vout))
        .hash())
}

/// `network` is the spark proto `Network` value, not an SDK-local ordinal. `target` is opaque
/// here — an issuer legitimately has none — but an attestor must bind one.
pub(crate) fn quote_envelope_digest_impl(
    network: u64,
    manifest_hash: &[u8],
    reason: u64,
    role: u64,
    target: &[u8],
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    validate_for_digest(network, manifest_hash, reason, role, target)
        .map_err(|msg| SparkTokenPrimitivesError::Spark(format!("quote envelope digest: {msg}")))?;

    Ok(Hasher::new(&QUOTE_TAG)
        .add_uint64(network)
        .add_bytes(manifest_hash)
        .add_uint64(reason)
        .add_uint64(role)
        .add_bytes(target)
        .hash())
}

#[cfg(test)]
mod tests;
