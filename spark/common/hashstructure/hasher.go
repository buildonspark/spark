package hashstructure

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"slices"
)

// Hasher provides a type-safe API for securely hashing a sequence of values with SHA256.
// It preserves collision resistance when tags differ.
// To preserve collision resistance across schema changes (such as data type, order, or meaning)
// add a version to the tag.
//
// Example:
//
//	hash := hashstructure.NewHasher([]string{"spark", "token", "version 5"}).
//		AddBytes([]byte("data")).
//		AddString("value").
//		AddUint64(123).
//		Hash()
type Hasher struct {
	hasher hash.Hash
}

// NewHasher creates a new Hasher with the given hierarchical domain tag.
// The tag is a hierarchical path, such as []string{"spark", "token", "create"}.
func NewHasher(tag []string) *Hasher {
	tagHash := sha256.Sum256(serializeTag(tag))

	hasher := sha256.New()
	// Write tagHash || tagHash as per BIP-340 tagged hash pattern
	hasher.Write(tagHash[:])
	hasher.Write(tagHash[:])

	return &Hasher{
		hasher: hasher,
	}
}

// AddBytes adds a []byte value to the hash computation.
func (h *Hasher) AddBytes(b []byte) *Hasher {
	h.addValue(b)
	return h
}

// AddString adds a string value to the hash computation.
func (h *Hasher) AddString(s string) *Hasher {
	h.addValue([]byte(s))
	return h
}

// AddUint adds a uint value to the hash computation.
func (h *Hasher) AddUint(v uint) *Hasher {
	return h.AddUint64(uint64(v))
}

// AddUint8 adds a uint8 value to the hash computation.
func (h *Hasher) AddUint8(v uint8) *Hasher {
	return h.AddUint64(uint64(v))
}

// AddUint16 adds a uint16 value to the hash computation.
func (h *Hasher) AddUint16(v uint16) *Hasher {
	return h.AddUint64(uint64(v))
}

// AddUint32 adds a uint32 value to the hash computation.
func (h *Hasher) AddUint32(v uint32) *Hasher {
	return h.AddUint64(uint64(v))
}

// AddUint64 adds a uint64 value to the hash computation.
func (h *Hasher) AddUint64(v uint64) *Hasher {
	valueBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(valueBytes, v)
	h.addValue(valueBytes)
	return h
}

// AddMapStringToBytes adds a map[string][]byte to the hash computation.
// The map is hashed in a deterministic order: first the count of entries,
// then each key-value pair sorted by key.
//
// Format: [count (uint64)] [key1 (string)] [value1 (bytes)] [key2 (string)] [value2 (bytes)] ...
func (h *Hasher) AddMapStringToBytes(m map[string][]byte) *Hasher {
	h.AddUint64(uint64(len(m)))

	// For determinism, convert map to slice of key-value pairs and sort by key
	type keyValuePair struct {
		key   string
		value []byte
	}
	pairs := make([]keyValuePair, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, keyValuePair{key: k, value: v})
	}
	slices.SortFunc(pairs, func(a, b keyValuePair) int { return cmp.Compare(a.key, b.key) })

	for _, pair := range pairs {
		h.AddString(pair.key)
		h.AddBytes(pair.value)
	}

	return h
}

// addValue writes a value directly to the hash state.
// Format: [8-byte length (big-endian uint64)] [value bytes]
func (h *Hasher) addValue(valueBytes []byte) {
	lengthBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(lengthBytes, uint64(len(valueBytes)))
	h.hasher.Write(lengthBytes)
	h.hasher.Write(valueBytes)
}

// Hash computes and returns the final SHA256 hash.
// The hash is computed using the BIP-340 tagged hash pattern:
//
//	tagHash = SHA256(serializeTag(tag))
//	result = SHA256(tagHash || tagHash || serialized values)
//
// Values are serialized incrementally as they are added via the Add* methods.
// Each value is serialized as [8-byte length (big-endian uint64)] [value bytes].
func (h *Hasher) Hash() []byte {
	return h.hasher.Sum(nil)
}

// serializeTag serializes a hierarchical tag into bytes.
// Format: For each component, [8-byte length (big-endian uint64)] [UTF-8 bytes]
func serializeTag(tag []string) []byte {
	var result []byte
	for _, component := range tag {
		componentBytes := []byte(component)
		result = binary.BigEndian.AppendUint64(result, uint64(len(componentBytes)))
		result = append(result, componentBytes...)
	}
	return result
}
