package uuids

import (
	"encoding/binary"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSlice(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	validUUID2 := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	tests := []struct {
		name  string
		input []string
		want  uuid.UUIDs
	}{
		{
			name:  "valid UUIDs",
			input: []string{validUUID1, validUUID2},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1), uuid.MustParse(validUUID2)},
		},
		{
			name:  "empty array",
			input: []string{},
			want:  uuid.UUIDs{},
		},
		{
			name:  "single valid UUID",
			input: []string{validUUID1},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSlice(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseSlice_Errors(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	invalidUUID := "invalid-uuid"

	tests := []struct {
		name  string
		input []string
	}{
		{
			name:  "invalid UUID",
			input: []string{invalidUUID},
		},
		{
			name:  "mixed valid and invalid UUIDs",
			input: []string{validUUID1, invalidUUID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSlice(tt.input)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

type testStruct struct {
	id string
}

func TestParseSliceFunc(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	validUUID2 := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	tests := []struct {
		name  string
		input []testStruct
		want  uuid.UUIDs
	}{
		{
			name:  "valid UUIDs",
			input: []testStruct{{id: validUUID1}, {id: validUUID2}},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1), uuid.MustParse(validUUID2)},
		},
		{
			name:  "empty array",
			input: []testStruct{},
			want:  uuid.UUIDs{},
		},
		{
			name:  "single valid UUID",
			input: []testStruct{{id: validUUID1}},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSliceFunc(tt.input, func(s testStruct) string { return s.id })
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseSliceFunc_Errors(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	invalidUUID := "invalid-uuid"

	tests := []struct {
		name  string
		input []testStruct
	}{
		{
			name:  "invalid UUID",
			input: []testStruct{{id: invalidUUID}},
		},
		{
			name:  "mixed valid and invalid UUIDs",
			input: []testStruct{{id: validUUID1}, {id: invalidUUID}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSliceFunc(tt.input, func(s testStruct) string { return s.id })
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestParseSeq(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	validUUID2 := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	tests := []struct {
		name  string
		input []string
		want  uuid.UUIDs
	}{
		{
			name:  "valid UUIDs",
			input: []string{validUUID1, validUUID2},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1), uuid.MustParse(validUUID2)},
		},
		{
			name:  "empty sequence",
			input: []string{},
			want:  uuid.UUIDs(nil),
		},
		{
			name:  "single valid UUID",
			input: []string{validUUID1},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSeq(slices.Values(tt.input))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseSeq_Errors(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	invalidUUID := "invalid-uuid"

	tests := []struct {
		name  string
		input []string
	}{
		{
			name:  "invalid UUID",
			input: []string{invalidUUID},
		},
		{
			name:  "mixed valid and invalid UUIDs",
			input: []string{validUUID1, invalidUUID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSeq(slices.Values(tt.input))
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestUUIDv7FromTime(t *testing.T) {
	t.Run("creates valid UUIDv7 with correct timestamp", func(t *testing.T) {
		now := time.Now()
		u := UUIDv7FromTime(now)

		// Check version is 7
		version := u[6] >> 4
		assert.EqualValues(t, 7, version, "UUID version should be 7")

		// Check variant is RFC4122 (10xx in binary)
		variant := u[8] >> 6
		assert.EqualValues(t, 2, variant, "UUID variant should be 2 (RFC4122)")

		// Extract timestamp from UUID and compare
		ms := binary.BigEndian.Uint64(u[:]) >> 16
		assert.EqualValues(t, now.UnixMilli(), ms, "Extracted timestamp should match input")
	})

	t.Run("creates boundary UUID with zeros for random bits", func(t *testing.T) {
		now := time.Now()
		u := UUIDv7FromTime(now)

		// Check that random bits in byte 6 (lower nibble) are 0
		assert.Zero(t, u[6]&0x0F, "Lower nibble of byte 6 should be 0")

		// Check that byte 7 is 0
		assert.Zero(t, u[7], "Byte 7 should be 0")

		// Check that lower 6 bits of byte 8 are 0
		assert.Zero(t, u[8]&0x3F, "Lower 6 bits of byte 8 should be 0")

		// Check that remaining bytes (9-15) are 0
		for i := 9; i < 16; i++ {
			assert.Zero(t, u[i], "Byte %d should be 0", i)
		}
	})

	t.Run("UUIDs are ordered by time", func(t *testing.T) {
		t1 := time.Now()
		time.Sleep(2 * time.Millisecond)
		t2 := time.Now()

		u1 := UUIDv7FromTime(t1)
		u2 := UUIDv7FromTime(t2)

		// Compare UUIDs as byte slices - u1 should be less than u2
		for i := range 16 {
			if u1[i] != u2[i] {
				assert.Less(t, u1[i], u2[i], "Earlier UUID should be less than later UUID")
				break
			}
		}
	})

	t.Run("handles past timestamps correctly", func(t *testing.T) {
		past := time.Now().Add(-1 * time.Hour)
		u := UUIDv7FromTime(past)

		ms := binary.BigEndian.Uint64(u[:]) >> 16
		assert.EqualValues(t, past.UnixMilli(), ms, "Past timestamp should be encoded correctly")
	})

	t.Run("handles future timestamps correctly", func(t *testing.T) {
		future := time.Now().Add(24 * time.Hour)
		u := UUIDv7FromTime(future)

		ms := binary.BigEndian.Uint64(u[:]) >> 16
		assert.EqualValues(t, future.UnixMilli(), ms, "Future timestamp should be encoded correctly")
	})

	t.Run("boundary UUID is less than actual UUIDv7 for same timestamp", func(t *testing.T) {
		now := time.Now()

		// Create multiple real UUIDv7s (they should have random bits set)
		realUUIDs := make([]uuid.UUID, 100)
		for i := range realUUIDs {
			realUUIDs[i] = uuid.Must(uuid.NewV7())
		}

		// Create boundary UUID for 1 second before now
		boundaryTime := now.Add(-1 * time.Second)
		boundaryUUID := UUIDv7FromTime(boundaryTime)

		// All real UUIDs (created now) should be greater than the boundary (created 1s ago)
		for i, realUUID := range realUUIDs {
			// Compare first 6 bytes (timestamp part)
			for j := range 6 {
				if boundaryUUID[j] != realUUID[j] {
					assert.Less(t, boundaryUUID[j], realUUID[j], "Boundary UUID should be less than real UUID %d at byte %d", i, j)
					break
				}
			}
		}
	})
}
