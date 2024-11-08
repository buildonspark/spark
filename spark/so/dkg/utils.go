package dkg

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"sort"

	"github.com/lightsparkdev/spark-go/so"
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

func signHash(privateKey *ecdsa.PrivateKey, hash []byte) ([]byte, error) {
	// Sign the hash
	sig, err := ecdsa.SignASN1(rand.Reader, privateKey, hash)
	if err != nil {
		return nil, err
	}

	return sig, nil
}

func SignRound1Packages(privateKey *ecdsa.PrivateKey, round1Packages []map[string][]byte) ([]byte, error) {
	hash := round1PackageHash(round1Packages)
	return signHash(privateKey, hash)
}

func ValidateRound1Signature(round1Packages []map[string][]byte, round1Signatures map[string][]byte, operatorMap map[string]*so.SigningOperator) (bool, []string) {
	hash := round1PackageHash(round1Packages)

	validationFailures := make([]string, 0)
	for identifier, operator := range operatorMap {
		signature, ok := round1Signatures[identifier]
		if !ok {
			validationFailures = append(validationFailures, identifier)
			continue
		}

		if !ecdsa.VerifyASN1(operator.IdentityPublicKey, hash, signature) {
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

func SignRound2Packages(privateKey *ecdsa.PrivateKey, round2Packages [][]byte) ([]byte, error) {
	hash := round2PackageHash(round2Packages)
	return signHash(privateKey, hash)
}
