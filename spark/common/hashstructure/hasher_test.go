package hashstructure

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
