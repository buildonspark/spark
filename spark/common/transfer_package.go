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
	if transferPackage.HashVariant == pb.HashVariant_HASH_VARIANT_V2 {
		return getTransferPackageSigningPayloadV2(transferID, transferPackage)
	}
	return getTransferPackageSigningPayloadLegacy(transferID, transferPackage)
}

func getTransferPackageSigningPayloadLegacy(transferID uuid.UUID, transferPackage *pb.TransferPackage) []byte {
	encryptedPayload := transferPackage.KeyTweakPackage
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

func getTransferPackageSigningPayloadV2(transferID uuid.UUID, transferPackage *pb.TransferPackage) []byte {
	return hashstructure.NewHasher([]string{"spark", "transfer", "signing payload"}).
		AddBytes(transferID[:]).
		AddMapStringToBytes(transferPackage.KeyTweakPackage).
		Hash()
}

// GetClaimPackageSigningPayload returns the signing payload for a claim key tweak package.
func GetClaimPackageSigningPayload(transferID uuid.UUID, keyTweakPackage map[string][]byte) []byte {
	return hashstructure.NewHasher([]string{"spark", "claim", "signing payload"}).
		AddBytes(transferID[:]).
		AddMapStringToBytes(keyTweakPackage).
		Hash()
}
