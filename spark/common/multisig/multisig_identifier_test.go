package multisig

import (
	"bytes"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/multisig"
	"github.com/stretchr/testify/require"
)

// Test keys derived from deterministic private keys for reproducible tests.
var (
	testPrivKey1 = keys.MustParsePrivateKeyHex("0000000000000000000000000000000000000000000000000000000000000001")
	testPrivKey2 = keys.MustParsePrivateKeyHex("0000000000000000000000000000000000000000000000000000000000000002")
	testPrivKey3 = keys.MustParsePrivateKeyHex("0000000000000000000000000000000000000000000000000000000000000003")

	testPubKey1 = testPrivKey1.Public().Serialize()
	testPubKey2 = testPrivKey2.Public().Serialize()
	testPubKey3 = testPrivKey3.Public().Serialize()

	// Pre-sorted for convenience in tests.
	testPubKeySorted = sortedKeys(testPubKey1, testPubKey2)
)

func sortedKeys(keys ...[]byte) [][]byte {
	out := make([][]byte, len(keys))
	copy(out, keys)
	sortKeys(out)
	return out
}

func TestValidateAndComputeMultisigIdentifier_Determinism(t *testing.T) {
	config := &pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: testPubKeySorted,
	}

	id1, err := ValidateAndComputeMultisigIdentifier(config)
	require.NoError(t, err)

	id2, err := ValidateAndComputeMultisigIdentifier(config)
	require.NoError(t, err)

	require.True(t, bytes.Equal(id1, id2))
	require.Len(t, id1, 32)
}

func TestValidateAndComputeMultisigIdentifier_UnsortedKeysRejected(t *testing.T) {
	sorted := sortedKeys(testPubKey1, testPubKey2)
	// Reverse to guarantee unsorted order.
	unsorted := [][]byte{sorted[1], sorted[0]}

	config := &pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: unsorted,
	}

	_, err := ValidateAndComputeMultisigIdentifier(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sorted lexicographically")
}

func TestValidateAndComputeMultisigIdentifier_NormalizedConfigProducesSameID(t *testing.T) {
	config := &pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: [][]byte{testPubKey1, testPubKey2},
	}
	reversed := &pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: [][]byte{testPubKey2, testPubKey1},
	}

	norm1 := NormalizeMultisigConfig(config)
	norm2 := NormalizeMultisigConfig(reversed)

	id1, err := ValidateAndComputeMultisigIdentifier(norm1)
	require.NoError(t, err)

	id2, err := ValidateAndComputeMultisigIdentifier(norm2)
	require.NoError(t, err)

	require.True(t, bytes.Equal(id1, id2))
}

func TestValidateAndComputeMultisigIdentifier_DifferentKeysDifferentID(t *testing.T) {
	config1 := NormalizeMultisigConfig(&pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: [][]byte{testPubKey1, testPubKey2},
	})

	config2 := NormalizeMultisigConfig(&pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: [][]byte{testPubKey3, testPubKey2},
	})

	id1, err := ValidateAndComputeMultisigIdentifier(config1)
	require.NoError(t, err)

	id2, err := ValidateAndComputeMultisigIdentifier(config2)
	require.NoError(t, err)

	require.False(t, bytes.Equal(id1, id2))
}

func TestValidateAndComputeMultisigIdentifier_DifferentThresholdsDifferentID(t *testing.T) {
	keys := sortedKeys(testPubKey1, testPubKey2, testPubKey3)

	config1 := &pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: keys,
	}

	config2 := &pb.MultisigConfig{
		Version:    0,
		Threshold:  3,
		PublicKeys: keys,
	}

	id1, err := ValidateAndComputeMultisigIdentifier(config1)
	require.NoError(t, err)

	id2, err := ValidateAndComputeMultisigIdentifier(config2)
	require.NoError(t, err)

	require.False(t, bytes.Equal(id1, id2))
}

func TestValidateAndComputeMultisigIdentifier_UnsupportedVersionRejected(t *testing.T) {
	config := &pb.MultisigConfig{
		Version:    1,
		Threshold:  2,
		PublicKeys: testPubKeySorted,
	}

	_, err := ValidateAndComputeMultisigIdentifier(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported version")
}

func TestValidateAndComputeMultisigIdentifier_ValidationErrors(t *testing.T) {
	tests := []struct {
		name      string
		config    *pb.MultisigConfig
		errSubstr string
	}{
		{
			name:      "nil config",
			config:    nil,
			errSubstr: "cannot be nil",
		},
		{
			name: "no keys",
			config: &pb.MultisigConfig{
				Version:    0,
				Threshold:  1,
				PublicKeys: [][]byte{},
			},
			errSubstr: "at least two",
		},
		{
			name: "single key",
			config: &pb.MultisigConfig{
				Version:    0,
				Threshold:  1,
				PublicKeys: [][]byte{testPubKey1},
			},
			errSubstr: "at least two",
		},
		{
			name: "zero threshold",
			config: &pb.MultisigConfig{
				Version:    0,
				Threshold:  0,
				PublicKeys: testPubKeySorted,
			},
			errSubstr: "threshold must be at least 1",
		},
		{
			name: "threshold exceeds keys",
			config: &pb.MultisigConfig{
				Version:    0,
				Threshold:  3,
				PublicKeys: testPubKeySorted,
			},
			errSubstr: "cannot exceed",
		},
		{
			name: "invalid key length",
			config: &pb.MultisigConfig{
				Version:    0,
				Threshold:  1,
				PublicKeys: [][]byte{make([]byte, 32), make([]byte, 32)},
			},
			errSubstr: "must be 33 bytes",
		},
		{
			name: "duplicate keys",
			config: &pb.MultisigConfig{
				Version:    0,
				Threshold:  1,
				PublicKeys: [][]byte{testPubKey1, testPubKey1},
			},
			errSubstr: "duplicate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateAndComputeMultisigIdentifier(tc.config)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errSubstr)
		})
	}
}

func TestValidateAndComputeMultisigIdentifier_NofN(t *testing.T) {
	keys := sortedKeys(testPubKey1, testPubKey2, testPubKey3)
	config := &pb.MultisigConfig{
		Version:    0,
		Threshold:  3,
		PublicKeys: keys,
	}

	id, err := ValidateAndComputeMultisigIdentifier(config)
	require.NoError(t, err)
	require.Len(t, id, 32)
}

func TestNormalizeMultisigConfig_SortsKeys(t *testing.T) {
	config := &pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: [][]byte{testPubKey2, testPubKey1},
	}

	normalized := NormalizeMultisigConfig(config)

	require.Negative(t, bytes.Compare(normalized.PublicKeys[0], normalized.PublicKeys[1]))
	require.Equal(t, config.Version, normalized.Version)
	require.Equal(t, config.Threshold, normalized.Threshold)
}

func TestNormalizeMultisigConfig_DoesNotMutateOriginal(t *testing.T) {
	original := [][]byte{testPubKey2, testPubKey1}
	config := &pb.MultisigConfig{
		Version:    0,
		Threshold:  2,
		PublicKeys: original,
	}

	_ = NormalizeMultisigConfig(config)

	require.True(t, bytes.Equal(config.PublicKeys[0], testPubKey2))
	require.True(t, bytes.Equal(config.PublicKeys[1], testPubKey1))
}

func TestNormalizeMultisigConfig_NilInput(t *testing.T) {
	require.Nil(t, NormalizeMultisigConfig(nil))
}
