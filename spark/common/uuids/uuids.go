// Package uuids contains utilities for dealing with UUIDs.
package uuids

import (
	"fmt"
	"iter"

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
