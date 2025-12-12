package so

import (
	"fmt"
)

// Identifier is the identifier of the signing operator, which is its index + 1 as 32-bytes big-endian value, in hex.
type Identifier = string

// IndexToIdentifier converts a uint32 index to a 32-byte identifier string.
// The index is incremented by 1 before conversion to ensure the identifier is not 0.
func IndexToIdentifier(index uint32) Identifier {
	as64Bit := uint64(index) + 1
	return fmt.Sprintf("%064x", as64Bit)
}
