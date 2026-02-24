package multisig

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/lightsparkdev/spark/common/protohash"
	pb "github.com/lightsparkdev/spark/proto/multisig"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// ValidateAndComputeMultisigIdentifier validates a MultisigConfig and computes its canonical identifier.
// Keys must already be sorted lexicographically; use NormalizeMultisigConfig
// to produce a correctly-ordered config before calling this function.
func ValidateAndComputeMultisigIdentifier(config *pb.MultisigConfig) ([]byte, error) {
	if config == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("config cannot be nil"))
	}
	if len(config.PublicKeys) < 2 {
		return nil, sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("config must have at least two public keys"))
	}
	if config.Threshold == 0 {
		return nil, sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("threshold must be at least 1"))
	}
	if config.Threshold > uint32(len(config.PublicKeys)) {
		return nil, sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("threshold (%d) cannot exceed number of keys (%d)", config.Threshold, len(config.PublicKeys)))
	}
	if config.Version != 0 {
		return nil, sparkerrors.InvalidArgumentInvalidVersion(fmt.Errorf("unsupported version: %d (only version 0 is supported)", config.Version))
	}

	for i, pk := range config.PublicKeys {
		if len(pk) != 33 {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("public key must be 33 bytes, got %d", len(pk)))
		}
		if i > 0 {
			cmp := bytes.Compare(config.PublicKeys[i-1], pk)
			if cmp == 0 {
				return nil, sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("duplicate public key"))
			}
			if cmp > 0 {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("public keys must be sorted lexicographically"))
			}
		}
	}

	return protohash.Hash(config)
}

// NormalizeMultisigConfig returns a copy of the config with public keys sorted
// lexicographically. This produces the canonical form expected by ValidateAndComputeMultisigIdentifier.
func NormalizeMultisigConfig(config *pb.MultisigConfig) *pb.MultisigConfig {
	if config == nil {
		return nil
	}
	sorted := make([][]byte, len(config.PublicKeys))
	copy(sorted, config.PublicKeys)
	sortKeys(sorted)
	return &pb.MultisigConfig{
		Version:    config.Version,
		Threshold:  config.Threshold,
		PublicKeys: sorted,
	}
}

func sortKeys(keys [][]byte) {
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i], keys[j]) < 0
	})
}
