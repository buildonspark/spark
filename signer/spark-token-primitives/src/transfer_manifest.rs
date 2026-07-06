//! Signed digest for a `TransferManifest`:
//!
//!   tagged_hash(["spark", "transfer", "manifest"], protohash(canonicalize(manifest)))
//!
//! This is the shared reference implementation consumed by the TS SDK (wasm)
//! and the SSP (uniffi); the Go operator keeps its own port in
//! `spark/common/transfer_manifest.go`. Parity across all of them is pinned by
//! the golden vectors in `spark/testdata/transfer_manifest_hash_cases.json` —
//! a rule change here must land in the Go port and re-mint those vectors.
//!
//! Canonicalization (applied to a copy; the input is never mutated):
//!   - edges are sorted by (sender, receiver, amount kind, amount value)
//!   - fees are sorted by (source, role, recipient, amount kind, amount value)
//!   - timestamps are floored to whole milliseconds, the precision every
//!     supported language can represent losslessly
//!
//! Validation is fail-closed at hash time because objecthash silently omits
//! zero-valued fields: an unset version, network, pubkey, or amount would
//! simply vanish from the digest instead of failing, so we reject them before
//! they can weaken what the signature covers.
//!
//! Validation here covers only what affects the digest's cross-language
//! meaning. Structural validity (UUID format, 33-byte keys) is owned by the
//! proto's validate.rules at the RPC boundary — duplicating those rules in
//! hand-written validators would drift.

use std::cmp::Ordering;

use prost::Message;
use prost_types::Timestamp;

use crate::hashstructure::Hasher;
use crate::proto::spark::{
    manifest_amount, FeeComponent, FeeRole, FeeSource, ManifestAmount, ManifestEdge, Network,
    TransferManifest,
};
use crate::protohash::hash_proto;
use crate::SparkTokenPrimitivesError;

const TRANSFER_MANIFEST_HASH_TAG: [&str; 3] = ["spark", "transfer", "manifest"];

// The only manifest format version this implementation can vouch for.
// protohash silently ignores unknown proto fields, so hashing a
// future-version manifest would sign a digest missing the fields this build
// doesn't know about — reject instead.
const SUPPORTED_TRANSFER_MANIFEST_VERSION: u32 = 1;

// Protobuf Timestamp bounds: [0, 9999-12-31T23:59:59Z]. Negative epochs are
// additionally rejected because JS's remainder operator would derive negative
// nanos from a pre-1970 Date, silently diverging from this digest.
const MAX_TIMESTAMP_SECONDS: i64 = 253402300799;

// Total bitcoin supply (21M BTC) in sats caps any real amount; it also sits
// below JS Number.MAX_SAFE_INTEGER, so the TS SDK's number-typed sats never
// lose precision on an accepted manifest.
const MAX_MANIFEST_AMOUNT_SATS: u64 = 2_100_000_000_000_000;

pub(crate) fn hash_transfer_manifest_impl(
    transfer_manifest_bytes: &[u8],
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    let manifest = TransferManifest::decode(transfer_manifest_bytes).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("failed to decode TransferManifest: {err}"))
    })?;

    validate_for_hashing(&manifest).map_err(|msg| {
        SparkTokenPrimitivesError::Spark(format!("transfer manifest not hashable: {msg}"))
    })?;

    let canonical = canonicalize(&manifest);
    let object_hash = hash_proto(&canonical).map_err(|err| {
        SparkTokenPrimitivesError::Spark(format!("hashing transfer manifest: {err}"))
    })?;

    Ok(Hasher::new(&TRANSFER_MANIFEST_HASH_TAG)
        .add_bytes(&object_hash)
        .hash())
}

fn validate_for_hashing(manifest: &TransferManifest) -> Result<(), String> {
    if manifest.version != SUPPORTED_TRANSFER_MANIFEST_VERSION {
        return Err(format!(
            "unsupported manifest version {} (supported: {})",
            manifest.version, SUPPORTED_TRANSFER_MANIFEST_VERSION
        ));
    }
    if manifest.transfer_id.is_empty() {
        return Err("transfer_id must be set".to_string());
    }
    // Signing an enum value this build's descriptor can't name is the same
    // hazard as an unknown manifest version, so unknown values are rejected
    // alongside the unset zero.
    if manifest.network == Network::Unspecified as i32
        || Network::try_from(manifest.network).is_err()
    {
        return Err(format!(
            "network must be a known value: {}",
            manifest.network
        ));
    }
    validate_timestamp(manifest.transfer_expiry_time.as_ref())
        .map_err(|msg| format!("transfer_expiry_time: {msg}"))?;
    validate_timestamp(manifest.quote_expiry_time.as_ref())
        .map_err(|msg| format!("quote_expiry_time: {msg}"))?;
    if manifest.edges.is_empty() {
        return Err("edges must be non-empty".to_string());
    }
    for (i, edge) in manifest.edges.iter().enumerate() {
        if edge.sender_identity_public_key.is_empty() {
            return Err(format!(
                "edges[{i}]: sender_identity_public_key must be set"
            ));
        }
        if edge.receiver_identity_public_key.is_empty() {
            return Err(format!(
                "edges[{i}]: receiver_identity_public_key must be set"
            ));
        }
        validate_amount(edge.amount.as_ref()).map_err(|msg| format!("edges[{i}]: {msg}"))?;
    }
    for (i, fee) in manifest.fees.iter().enumerate() {
        if fee.source == FeeSource::Unspecified as i32 || FeeSource::try_from(fee.source).is_err() {
            return Err(format!(
                "fees[{i}]: source must be a known value: {}",
                fee.source
            ));
        }
        if FeeRole::try_from(fee.role).is_err() {
            return Err(format!(
                "fees[{i}]: role must be a known value: {}",
                fee.role
            ));
        }
        if fee.source == FeeSource::PartnerMarkup as i32 && fee.role == FeeRole::Unspecified as i32
        {
            return Err(format!(
                "fees[{i}]: role must be set for partner markup fees"
            ));
        }
        validate_amount(fee.amount.as_ref()).map_err(|msg| format!("fees[{i}]: {msg}"))?;
    }
    Ok(())
}

fn validate_timestamp(ts: Option<&Timestamp>) -> Result<(), String> {
    let Some(ts) = ts else {
        return Ok(());
    };
    if ts.seconds < 0 || ts.seconds > MAX_TIMESTAMP_SECONDS {
        return Err(format!(
            "seconds out of range [0, {MAX_TIMESTAMP_SECONDS}]: {}",
            ts.seconds
        ));
    }
    if ts.nanos < 0 || ts.nanos >= 1_000_000_000 {
        return Err(format!("nanos out of range [0, 1e9): {}", ts.nanos));
    }
    // Objecthash implementations disagree on present-but-zero message fields:
    // this crate's default-skipping gate omits a present epoch timestamp
    // entirely, while presence-gated implementations (the Go port) hash it as
    // list[0,0] — reject rather than sign a digest implementations disagree on.
    if ts.seconds == 0 && ts.nanos < 1_000_000 {
        return Err(
            "timestamp must not floor to the epoch (implementations disagree on present-but-zero fields)"
                .to_string(),
        );
    }
    Ok(())
}

fn validate_amount(amount: Option<&ManifestAmount>) -> Result<(), String> {
    let (kind, value) = amount_sort_key(amount);
    if kind == 0 {
        return Err("amount must be set".to_string());
    }
    if value == 0 {
        return Err("amount must be non-zero (zero is omitted from the hash)".to_string());
    }
    if kind == 1 && value > MAX_MANIFEST_AMOUNT_SATS {
        return Err(format!(
            "sats amount exceeds {MAX_MANIFEST_AMOUNT_SATS}: {value}"
        ));
    }
    Ok(())
}

fn canonicalize(manifest: &TransferManifest) -> TransferManifest {
    let mut canonical = manifest.clone();
    canonical.edges.sort_by(compare_edges);
    canonical.fees.sort_by(compare_fees);
    canonical.transfer_expiry_time = canonical.transfer_expiry_time.map(floor_to_millis);
    canonical.quote_expiry_time = canonical.quote_expiry_time.map(floor_to_millis);
    canonical
}

fn compare_edges(a: &ManifestEdge, b: &ManifestEdge) -> Ordering {
    a.sender_identity_public_key
        .cmp(&b.sender_identity_public_key)
        .then_with(|| {
            a.receiver_identity_public_key
                .cmp(&b.receiver_identity_public_key)
        })
        .then_with(|| compare_amounts(a.amount.as_ref(), b.amount.as_ref()))
}

fn compare_fees(a: &FeeComponent, b: &FeeComponent) -> Ordering {
    a.source
        .cmp(&b.source)
        .then_with(|| a.role.cmp(&b.role))
        .then_with(|| {
            a.recipient_identity_public_key
                .cmp(&b.recipient_identity_public_key)
        })
        .then_with(|| compare_amounts(a.amount.as_ref(), b.amount.as_ref()))
}

fn compare_amounts(a: Option<&ManifestAmount>, b: Option<&ManifestAmount>) -> Ordering {
    amount_sort_key(a).cmp(&amount_sort_key(b))
}

// Keys an amount by (oneof field number, value); kind 0 means unset.
fn amount_sort_key(amount: Option<&ManifestAmount>) -> (u32, u64) {
    match amount.and_then(|a| a.amount.as_ref()) {
        Some(manifest_amount::Amount::Sats(sats)) => (1, *sats),
        Some(manifest_amount::Amount::Bps(bps)) => (2, u64::from(*bps)),
        None => (0, 0),
    }
}

fn floor_to_millis(ts: Timestamp) -> Timestamp {
    Timestamp {
        seconds: ts.seconds,
        nanos: (ts.nanos / 1_000_000) * 1_000_000,
    }
}

#[cfg(test)]
mod tests;
