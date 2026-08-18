package common

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"

	"github.com/btcsuite/btcd/btcutil"

	"github.com/lightsparkdev/spark/common/hashstructure"
	"github.com/lightsparkdev/spark/common/protohash"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var transferManifestHashTag = []string{"spark", "transfer", "manifest"}

// SupportedTransferManifestVersion is the only manifest format version this
// implementation can vouch for. protohash silently ignores unknown proto
// fields, so hashing a future-version manifest would sign a digest missing
// the fields this binary doesn't know about — reject instead.
const SupportedTransferManifestVersion = 1

// Protobuf Timestamp bounds: [0, 9999-12-31T23:59:59Z]. Negative epochs are
// additionally rejected because JS's remainder operator would derive negative
// nanos from a pre-1970 Date, silently diverging from Go's digest.
const maxTimestampSeconds = 253402300799

const compressedPublicKeyLen = 33

// HashTransferManifest returns the signed digest for a TransferManifest:
//
//	tagged_hash(["spark", "transfer", "manifest"], protohash(canonicalize(manifest)))
//
// Every manifest signer and verifier (SDK, SSP, SO) must hash through this
// function; the canonicalization below is part of the signed contract and is
// pinned by cross-language golden vectors in testdata/transfer_manifest_hash_cases.json.
// Byte-for-byte ports of this construction live in the TS SDK
// (spark-sdk src/utils/manifest-hashing.ts) and in the SSP's internal
// implementation — a rule change here must land in every port and re-mint
// the golden vectors.
//
// Canonicalization (applied to a copy; the input is never mutated):
//   - edges are sorted by (sender, receiver, amount kind, amount value)
//   - fees are sorted by (source, role, receiver, amount kind, amount value)
//   - timestamps are floored to whole milliseconds, the precision every
//     supported language can represent losslessly
//
// Validation is fail-closed at hash time because objecthash silently omits
// zero-valued fields: an unset version, network, pubkey, or amount would
// simply vanish from the digest instead of failing, so we reject them before
// they can weaken what the signature covers.
//
// Validation here covers what affects the digest's cross-language meaning,
// plus the fee receiver's length: nothing downstream ever parses a fee key, so
// an ill-formed one would otherwise reach a signature unchecked. Edge keys are
// parsed where they are spent, and UUID format stays with the proto.
func HashTransferManifest(manifest *pb.TransferManifest) ([]byte, error) {
	if err := validateTransferManifestForHashing(manifest); err != nil {
		return nil, fmt.Errorf("transfer manifest not hashable: %w", err)
	}

	canonical := canonicalizeTransferManifest(manifest)
	objectHash, err := protohash.Hash(canonical)
	if err != nil {
		return nil, fmt.Errorf("hashing transfer manifest: %w", err)
	}

	return hashstructure.NewHasher(transferManifestHashTag).AddBytes(objectHash).Hash(), nil
}

func validateTransferManifestForHashing(manifest *pb.TransferManifest) error {
	if manifest == nil {
		return fmt.Errorf("manifest is nil")
	}
	if manifest.GetVersion() != SupportedTransferManifestVersion {
		return fmt.Errorf("unsupported manifest version %d (supported: %d)", manifest.GetVersion(), SupportedTransferManifestVersion)
	}
	if manifest.GetTransferId() == "" {
		return fmt.Errorf("transfer_id must be set")
	}
	if !isSignableProtoNetwork(manifest.GetNetwork()) {
		return fmt.Errorf("network must be a known value: %d", manifest.GetNetwork())
	}
	if err := validateManifestTimestamp(manifest.GetTransferExpiryTime()); err != nil {
		return fmt.Errorf("transfer_expiry_time: %w", err)
	}
	if err := validateManifestTimestamp(manifest.GetQuoteExpiryTime()); err != nil {
		return fmt.Errorf("quote_expiry_time: %w", err)
	}
	if len(manifest.GetEdges()) == 0 {
		return fmt.Errorf("edges must be non-empty")
	}
	for i, edge := range manifest.GetEdges() {
		if len(edge.GetSenderIdentityPublicKey()) == 0 {
			return fmt.Errorf("edges[%d]: sender_identity_public_key must be set", i)
		}
		if len(edge.GetReceiverIdentityPublicKey()) == 0 {
			return fmt.Errorf("edges[%d]: receiver_identity_public_key must be set", i)
		}
		if err := validateManifestAmount(edge.GetAmount()); err != nil {
			return fmt.Errorf("edges[%d]: %w", i, err)
		}
	}
	for i, fee := range manifest.GetFees() {
		if fee.GetSource() == pb.FeeSource_FEE_SOURCE_UNSPECIFIED || !isKnownEnumValue(pb.FeeSource_name, int32(fee.GetSource())) {
			return fmt.Errorf("fees[%d]: source must be a known value: %d", i, fee.GetSource())
		}
		if !isKnownEnumValue(pb.FeeRole_name, int32(fee.GetRole())) {
			return fmt.Errorf("fees[%d]: role must be a known value: %d", i, fee.GetRole())
		}
		if fee.GetSource() == pb.FeeSource_FEE_SOURCE_PARTNER_MARKUP && fee.GetRole() == pb.FeeRole_FEE_ROLE_UNSPECIFIED {
			return fmt.Errorf("fees[%d]: role must be set for partner markup fees", i)
		}
		if len(fee.GetReceiverIdentityPublicKey()) != compressedPublicKeyLen {
			return fmt.Errorf("fees[%d]: receiver_identity_public_key must be %d bytes", i, compressedPublicKeyLen)
		}
		if err := validateManifestAmount(fee.GetAmount()); err != nil {
			return fmt.Errorf("fees[%d]: %w", i, err)
		}
	}
	return nil
}

// isKnownEnumValue reports whether the value exists in the generated
// name map — i.e. this build's descriptor can vouch for it. Signing an
// enum value we don't understand is the same hazard as an unknown
// manifest version.
func isKnownEnumValue(nameMap map[int32]string, value int32) bool {
	_, ok := nameMap[value]
	return ok
}

// The nanos range check is enforceable only in Go: ts-proto's decode folds
// seconds+nanos into a Date before validation can see the raw fields, so a
// spec-invalid wire timestamp (nanos outside [0,1e9)) normalizes silently
// in TS but is rejected here. Fail-closed on the enforcement side.
//
// Producers MUST emit whole-millisecond timestamps on the wire. For those,
// accept domains and digests are identical in every language and transport.
// Sub-millisecond nanos are floored here as defensive normalization, but
// cross-language agreement is not guaranteed for them (ts-proto's binary
// decode float-rounds where JSON parsing truncates); any such divergence
// fails closed as a signature mismatch, never a forged acceptance.
func validateManifestTimestamp(ts *timestamppb.Timestamp) error {
	if ts == nil {
		return nil
	}
	if ts.GetSeconds() < 0 || ts.GetSeconds() > maxTimestampSeconds {
		return fmt.Errorf("seconds out of range [0, %d]: %d", int64(maxTimestampSeconds), ts.GetSeconds())
	}
	if ts.GetNanos() < 0 || ts.GetNanos() >= 1_000_000_000 {
		return fmt.Errorf("nanos out of range [0, 1e9): %d", ts.GetNanos())
	}
	// Objecthash implementations disagree on present-but-zero message fields:
	// this hasher's presence gate hashes a present epoch timestamp as
	// list[0,0], while default-skipping implementations omit it entirely —
	// reject rather than sign a digest implementations disagree on.
	if ts.GetSeconds() == 0 && ts.GetNanos() < 1_000_000 {
		return fmt.Errorf("timestamp must not floor to the epoch (implementations disagree on present-but-zero fields)")
	}
	return nil
}

func validateManifestAmount(amount *pb.ManifestAmount) error {
	kind, value := manifestAmountSortKey(amount)
	if kind == 0 {
		return fmt.Errorf("amount must be set")
	}
	if value == 0 {
		return fmt.Errorf("amount must be non-zero (zero is omitted from the hash)")
	}
	// Total bitcoin supply caps any real amount; it also sits below JS
	// Number.MAX_SAFE_INTEGER, so the TS SDK's number-typed sats never
	// lose precision on an accepted manifest.
	if kind == 1 && value > uint64(btcutil.MaxSatoshi) {
		return fmt.Errorf("sats amount exceeds %d: %d", int64(btcutil.MaxSatoshi), value)
	}
	return nil
}

func canonicalizeTransferManifest(manifest *pb.TransferManifest) *pb.TransferManifest {
	// Direct assertion: proto.Clone preserves the dynamic type, and a panic
	// here is deterministic and points at the real bug, unlike the nil
	// dereference a discarded ok would defer to the first field write.
	canonical := proto.Clone(manifest).(*pb.TransferManifest)

	slices.SortFunc(canonical.GetEdges(), compareManifestEdges)
	slices.SortFunc(canonical.GetFees(), compareFeeComponents)
	canonical.TransferExpiryTime = floorTimestampToMillis(canonical.GetTransferExpiryTime())
	canonical.QuoteExpiryTime = floorTimestampToMillis(canonical.GetQuoteExpiryTime())

	return canonical
}

func compareManifestEdges(a, b *pb.ManifestEdge) int {
	if c := bytes.Compare(a.GetSenderIdentityPublicKey(), b.GetSenderIdentityPublicKey()); c != 0 {
		return c
	}
	if c := bytes.Compare(a.GetReceiverIdentityPublicKey(), b.GetReceiverIdentityPublicKey()); c != 0 {
		return c
	}
	return compareManifestAmounts(a.GetAmount(), b.GetAmount())
}

func compareFeeComponents(a, b *pb.FeeComponent) int {
	if c := cmp.Compare(a.GetSource(), b.GetSource()); c != 0 {
		return c
	}
	if c := cmp.Compare(a.GetRole(), b.GetRole()); c != 0 {
		return c
	}
	if c := bytes.Compare(a.GetReceiverIdentityPublicKey(), b.GetReceiverIdentityPublicKey()); c != 0 {
		return c
	}
	return compareManifestAmounts(a.GetAmount(), b.GetAmount())
}

func compareManifestAmounts(a, b *pb.ManifestAmount) int {
	aKind, aValue := manifestAmountSortKey(a)
	bKind, bValue := manifestAmountSortKey(b)
	if c := cmp.Compare(aKind, bKind); c != 0 {
		return c
	}
	return cmp.Compare(aValue, bValue)
}

// manifestAmountSortKey keys an amount by (oneof field number, value); kind 0
// means unset.
func manifestAmountSortKey(amount *pb.ManifestAmount) (int, uint64) {
	switch amt := amount.GetAmount().(type) {
	case *pb.ManifestAmount_Sats:
		return 1, amt.Sats
	case *pb.ManifestAmount_Bps:
		return 2, uint64(amt.Bps)
	default:
		return 0, 0
	}
}

func floorTimestampToMillis(ts *timestamppb.Timestamp) *timestamppb.Timestamp {
	if ts == nil {
		return nil
	}
	return &timestamppb.Timestamp{
		Seconds: ts.GetSeconds(),
		Nanos:   (ts.GetNanos() / 1_000_000) * 1_000_000,
	}
}
