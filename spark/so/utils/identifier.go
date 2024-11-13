package utils

import "encoding/hex"

// indexToIdentifier converts a uint64 index to a 32-byte identifier string.
// The index is incremented by 1 before conversion to ensure identifier is not 0.
func IndexToIdentifier(index uint64) string {
	index = index + 1
	identifierBytes := make([]byte, 32)
	for i := 0; i < 8; i++ {
		identifierBytes[31-i] = byte(index >> (i * 8))
	}
	return hex.EncodeToString(identifierBytes)
}
