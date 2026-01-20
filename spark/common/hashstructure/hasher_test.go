package hashstructure

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasher_DomainSeparation(t *testing.T) {
	tag1 := []string{"spark", "token", "create"}
	hash1 := NewHasher(tag1).
		AddBytes([]byte{1, 2, 3}).
		Hash()

	tag2 := []string{"spark", "token", "mint"}
	hash2 := NewHasher(tag2).
		AddBytes([]byte{1, 2, 3}).
		Hash()

	assert.NotEqual(t, hash1, hash2)
}

func TestHasher_EmptyValues(t *testing.T) {
	tag := []string{"test"}

	hashNone := NewHasher(tag).Hash()

	hashOneEmpty := NewHasher(tag).
		AddBytes([]byte{}).
		Hash()

	assert.NotEqual(t, hashNone, hashOneEmpty)
}

func TestHasher_Nil(t *testing.T) {
	tag := []string{"test"}

	hashOneNil := NewHasher(tag).
		AddBytes(nil).
		Hash()

	hashOneEmpty := NewHasher(tag).
		AddBytes([]byte{}).
		Hash()

	assert.Equal(t, hashOneNil, hashOneEmpty)
}

func TestHasher_OrderMatters(t *testing.T) {
	tag := []string{"test"}

	hash1 := NewHasher(tag).
		AddUint32(1).
		AddUint32(2).
		Hash()

	hash2 := NewHasher(tag).
		AddUint32(2).
		AddUint32(1).
		Hash()

	assert.NotEqual(t, hash1, hash2)
}

func TestHasher_Deterministic(t *testing.T) {
	tag := []string{"spark", "operator", "sign"}

	hashes := make([][]byte, 2)

	for i := range hashes {
		hashes[i] = NewHasher(tag).
			AddUint32(123).
			AddBytes([]byte{0x01, 0x02, 0x03, 0x04}).
			AddString("transaction-id").
			AddBytes([]byte{0xFF, 0xFE}).
			Hash()
	}

	assert.Equal(t, hashes[0], hashes[1])
}

func TestHasher_TestVectors(t *testing.T) {
	type testCase struct {
		name     string
		expected string // hex-encoded expected hash
		actual   []byte
	}

	testCases := []testCase{
		{
			name:     "empty tag",
			expected: "2dba5dbc339e7316aea2683faf839c1b7b1ee2313db792112588118df066aa35",
			actual: NewHasher([]string{}).
				Hash(),
		},
		{
			name:     "empty data",
			expected: "c67afb9eb635e689553aefb4366b06372478967e813c0261377067f38257d48f",
			actual: NewHasher([]string{"test", "vector"}).
				Hash(),
		},
		{
			name:     "all data types",
			expected: "7e6d5afa6426b20f2f08929260f3e37cf280497eae6d29c1af313db01a8996a8",
			actual: NewHasher([]string{"test", "vector"}).
				AddBytes([]byte{1, 2, 3}).
				AddString("string").
				AddUint(1).
				AddUint8(8).
				AddUint16(16).
				AddUint32(32).
				AddUint64(64).
				Hash(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			expectedBytes, err := hex.DecodeString(tc.expected)
			require.NoError(t, err, "invalid expected hex")
			assert.Equal(t, expectedBytes, tc.actual, "mismatch for test vector %q, got %s", tc.name, hex.EncodeToString(tc.actual))
		})
	}
}
