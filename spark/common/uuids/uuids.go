// Package uuids contains utilities for dealing with UUIDs.
package uuids

import (
	"fmt"
	"iter"
	"time"

	"github.com/google/uuid"
)

// ParseSlice parses a slice of strings representing UUIDs. It returns an error if any of the UUIDs is invalid.
func ParseSlice(arr []string) (uuid.UUIDs, error) {
	results := make(uuid.UUIDs, len(arr))
	for i, v := range arr {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("unable to parse %q as a UUID: %w", v, err)
		}
		results[i] = id
	}
	return results, nil
}

// ParseSliceFunc parses a slice of values representing UUIDs, using fn to transform the values into strings for parsing.
// It returns an error if any of the UUIDs is invalid.
func ParseSliceFunc[K any](arr []K, fn func(K) string) (uuid.UUIDs, error) {
	results := make(uuid.UUIDs, len(arr))
	for i, v := range arr {
		raw := fn(v)
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("unable to parse %q as a UUID: %w", raw, err)
		}
		results[i] = id
	}
	return results, nil
}

// ParseSeq parses an [iter.Seq] of strings representing UUIDs. It returns an error if any of the UUIDs is invalid.
func ParseSeq(seq iter.Seq[string]) (uuid.UUIDs, error) {
	var results uuid.UUIDs
	for v := range seq {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("unable to parse %q as a UUID: %w", v, err)
		}
		results = append(results, id)
	}
	return results, nil
}

// UUIDv7FromTime creates a UUIDv7 boundary value from a timestamp.
// The resulting UUID has the timestamp encoded in the first 48 bits and zeros for the random bits.
// This is useful for range queries on UUIDv7 fields.
func UUIDv7FromTime(t time.Time) uuid.UUID {
	// UUIDv7 format:
	// - 48 bits: Unix timestamp in milliseconds
	// - 4 bits: version (0x7)
	// - 12 bits: random/counter (set to 0 for boundary)
	// - 2 bits: variant (0b10)
	// - 62 bits: random (set to 0 for boundary)

	var u uuid.UUID

	// Get milliseconds since Unix epoch
	ms := uint64(t.UnixMilli())

	// Encode timestamp in first 48 bits (6 bytes)
	u[0] = byte(ms >> 40)
	u[1] = byte(ms >> 32)
	u[2] = byte(ms >> 24)
	u[3] = byte(ms >> 16)
	u[4] = byte(ms >> 8)
	u[5] = byte(ms)

	// Set version to 7 (byte 6, high nibble)
	u[6] = 0x70 // 0111 0000

	// Set variant to RFC4122 (byte 8, high 2 bits = 10)
	u[8] = 0x80 // 1000 0000

	// All other bits remain 0 (for minimum boundary)
	return u
}
