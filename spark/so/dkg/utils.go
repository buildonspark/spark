package dkg

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so"
)

func round1PackageHash(maps []map[string][]byte) []byte {
	// For each map, create a deterministic string representation
	mapHashes := make([][]byte, len(maps))

	for i, m := range maps {
		// Get all keys from the map
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys) // Only sort keys within each map

		// Create a hash for this map
		hasher := sha256.New()
		for _, k := range keys {
			hasher.Write([]byte(k))
			hasher.Write(m[k])
		}

		mapHashes[i] = hasher.Sum(nil)
	}

	// Calculate final hash preserving array order
	finalHasher := sha256.New()
	for _, hash := range mapHashes {
		finalHasher.Write(hash)
	}

	return finalHasher.Sum(nil)
}

func signHash(privateKey []byte, hash []byte) ([]byte, error) {
	priv := secp256k1.PrivKeyFromBytes(privateKey)

	// Sign the hash
	sig := ecdsa.Sign(priv, hash)

	return sig.Serialize(), nil
}

func signRound1Packages(privateKey []byte, round1Packages []map[string][]byte) ([]byte, error) {
	hash := round1PackageHash(round1Packages)
	return signHash(privateKey, hash)
}

func validateRound1Signature(round1Packages []map[string][]byte, round1Signatures map[string][]byte, operatorMap map[string]*so.SigningOperator) (bool, []string) {
	hash := round1PackageHash(round1Packages)

	validationFailures := make([]string, 0)
	for identifier, operator := range operatorMap {
		signature, ok := round1Signatures[identifier]
		if !ok {
			validationFailures = append(validationFailures, identifier)
			continue
		}

		pub, err := secp256k1.ParsePubKey(operator.IdentityPublicKey)
		if err != nil {
			validationFailures = append(validationFailures, identifier)
			continue
		}

		sig, err := ecdsa.ParseDERSignature(signature)
		if err != nil {
			validationFailures = append(validationFailures, identifier)
			continue
		}

		if !sig.Verify(hash, pub) {
			validationFailures = append(validationFailures, identifier)
		}
	}

	return len(validationFailures) == 0, validationFailures
}

func round2PackageHash(round2Packages [][]byte) []byte {
	hasher := sha256.New()
	for _, p := range round2Packages {
		hasher.Write(p)
	}
	return hasher.Sum(nil)
}

func signRound2Packages(privateKey []byte, round2Packages [][]byte) ([]byte, error) {
	hash := round2PackageHash(round2Packages)
	return signHash(privateKey, hash)
}

func deriveKeyIndex(batchID uuid.UUID, index uint16) uuid.UUID {
	derivedID := batchID
	// Write the index to the last 2 bytes
	binary.BigEndian.PutUint16(derivedID[14:], index)
	return derivedID
}
