package common

import (
	"cmp"
	"crypto/sha256"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/hashstructure"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// GetTransferPackageSigningPayload returns the signing payload for a transfer package.
// The payload is a hash of the transfer ID and the encrypted payload sorted by key.
func GetTransferPackageSigningPayload(transferID uuid.UUID, transferPackage *pb.TransferPackage) []byte {
	switch transferPackage.GetHashVariant() {
	case pb.HashVariant_HASH_VARIANT_V2:
		return getTransferPackageSigningPayloadV2(transferID, transferPackage)
	default:
		return getTransferPackageSigningPayloadLegacy(transferID, transferPackage)
	}
}

func getTransferPackageSigningPayloadLegacy(transferID uuid.UUID, transferPackage *pb.TransferPackage) []byte {
	encryptedPayload := transferPackage.GetKeyTweakPackage()
	// Create a slice to hold the sorted key-value pairs
	type keyValuePair struct {
		key   string
		value []byte
	}

	// Convert map to slice of key-value pairs
	pairs := make([]keyValuePair, 0, len(encryptedPayload))
	for k, v := range encryptedPayload {
		pairs = append(pairs, keyValuePair{key: k, value: v})
	}

	// Sort the slice by key to ensure deterministic ordering
	// This is important for consistent signing payloads
	slices.SortFunc(pairs, func(a, b keyValuePair) int { return cmp.Compare(a.key, b.key) })

	hasher := sha256.New()
	hasher.Write(transferID[:])
	for _, pair := range pairs {
		hasher.Write([]byte(pair.key + ":"))
		hasher.Write(pair.value)
		hasher.Write([]byte(";"))
	}

	return hasher.Sum(nil)
}

// getTransferPackageSigningPayloadV2 binds the transfer id and the key tweak
// package, plus the delegation intent (Spark Pull) when one is present: the
// cited grant, the delegate authorizing the spend, and the exact amount flowing
// to each receiver. Every SO can then verify a delegated spend against the grant
// it claims to spend under.
//
// The intent fields are appended only when an intent is present, so a transfer
// without one hashes exactly as it did before delegation existed and every
// already-released SDK keeps verifying. Appending them unconditionally would
// change the digest of every transfer, including those with no intent, and
// require every client and operator to cut over at the same instant.
//
// Presence is therefore load-bearing: an intent that is set but empty is a
// different signed statement from no intent at all, and both the Go and the
// TypeScript signer must agree on that predicate exactly.
func getTransferPackageSigningPayloadV2(transferID uuid.UUID, transferPackage *pb.TransferPackage) []byte {
	h := hashstructure.NewHasher([]string{"spark", "transfer", "signing payload"}).
		AddBytes(transferID[:]).
		AddMapStringToBytes(transferPackage.GetKeyTweakPackage())

	if intent := transferPackage.GetDelegationIntent(); intent != nil {
		h.AddString(intent.GetGrantId()).
			AddBytes(intent.GetSpenderIdentityPublicKey()).
			AddUint64(intent.GetTotalAmountSats()).
			AddMapStringToUint64(intent.GetReceiverAmountsSats())
	}
	return h.Hash()
}

// GetClaimPackageSigningPayload returns the signing payload for a claim key tweak package.
func GetClaimPackageSigningPayload(transferID uuid.UUID, keyTweakPackage map[string][]byte) []byte {
	return hashstructure.NewHasher([]string{"spark", "claim", "signing payload"}).
		AddBytes(transferID[:]).
		AddMapStringToBytes(keyTweakPackage).
		Hash()
}
