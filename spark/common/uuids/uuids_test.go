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

func TestUUIDRangeForDate(t *testing.T) {
	t.Run("creates correct range for specific date", func(t *testing.T) {
		// Test with a specific known date: 2024-01-15
		date := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
		from, to := UUIDRangeForDate(date)

		// Expected timestamps
		expectedFromMs := date.UnixMilli()                   // 2024-01-15 00:00:00 UTC
		expectedToMs := date.Add(24 * time.Hour).UnixMilli() // 2024-01-16 00:00:00 UTC

		// Extract timestamps from UUIDs
		fromMs := binary.BigEndian.Uint64(from[:]) >> 16
		toMs := binary.BigEndian.Uint64(to[:]) >> 16

		assert.EqualValues(t, expectedFromMs, fromMs, "From timestamp should be start of day")
		assert.EqualValues(t, expectedToMs, toMs, "To timestamp should be start of next day")

		// Verify exact UUID values for the known date
		expectedFrom := uuid.MustParse("018d0a6a-fc00-7000-8000-000000000000")
		expectedTo := uuid.MustParse("018d0f91-5800-7000-8000-000000000000")

		assert.Equal(t, expectedFrom, from, "From UUID should match expected value")
		assert.Equal(t, expectedTo, to, "To UUID should match expected value")
	})

	t.Run("range spans exactly 24 hours", func(t *testing.T) {
		date := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
		from, to := UUIDRangeForDate(date)

		fromMs := binary.BigEndian.Uint64(from[:]) >> 16
		toMs := binary.BigEndian.Uint64(to[:]) >> 16

		durationMs := toMs - fromMs
		assert.EqualValues(t, 24*60*60*1000, durationMs, "Range should span exactly 24 hours")
	})

	t.Run("ranges are contiguous across days", func(t *testing.T) {
		day1 := time.Date(2024, 3, 20, 0, 0, 0, 0, time.UTC)
		day2 := day1.Add(24 * time.Hour)

		_, to1 := UUIDRangeForDate(day1)
		from2, _ := UUIDRangeForDate(day2)

		assert.Equal(t, to1, from2, "End of day1 should equal start of day2")
	})

	t.Run("normalizes to UTC regardless of input timezone", func(t *testing.T) {
		// All three times fall on the same UTC calendar day (2024-05-10),
		// even though they are different instants and expressed in different timezones.
		utcTime := time.Date(2024, 5, 10, 0, 0, 0, 0, time.UTC)
		estTime := time.Date(2024, 5, 9, 20, 0, 0, 0, time.FixedZone("EST", -5*3600))
		pstTime := time.Date(2024, 5, 9, 17, 0, 0, 0, time.FixedZone("PST", -8*3600))

		fromUTC, toUTC := UUIDRangeForDate(utcTime)
		fromEST, toEST := UUIDRangeForDate(estTime)
		fromPST, toPST := UUIDRangeForDate(pstTime)

		// All should produce the same range when normalized to UTC date
		assert.Equal(t, fromUTC, fromEST, "EST should normalize to same UTC range")
		assert.Equal(t, toUTC, toEST, "EST should normalize to same UTC range")
		assert.Equal(t, fromUTC, fromPST, "PST should normalize to same UTC range")
		assert.Equal(t, toUTC, toPST, "PST should normalize to same UTC range")
	})

	t.Run("handles midnight time correctly", func(t *testing.T) {
		// Passing exactly midnight should produce same result as any time during that day
		midnight := time.Date(2024, 7, 4, 0, 0, 0, 0, time.UTC)
		noon := time.Date(2024, 7, 4, 12, 0, 0, 0, time.UTC)
		almostMidnight := time.Date(2024, 7, 4, 23, 59, 59, 999999999, time.UTC)

		fromMidnight, toMidnight := UUIDRangeForDate(midnight)
		fromNoon, toNoon := UUIDRangeForDate(noon)
		fromAlmost, toAlmost := UUIDRangeForDate(almostMidnight)

		assert.Equal(t, fromMidnight, fromNoon, "Any time on same day should produce same range")
		assert.Equal(t, toMidnight, toNoon, "Any time on same day should produce same range")
		assert.Equal(t, fromMidnight, fromAlmost, "Any time on same day should produce same range")
		assert.Equal(t, toMidnight, toAlmost, "Any time on same day should produce same range")
	})

	t.Run("handles Unix epoch correctly", func(t *testing.T) {
		epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		from, to := UUIDRangeForDate(epoch)

		fromMs := binary.BigEndian.Uint64(from[:]) >> 16
		toMs := binary.BigEndian.Uint64(to[:]) >> 16

		assert.EqualValues(t, 0, fromMs, "Unix epoch should have timestamp 0")
		assert.EqualValues(t, 24*60*60*1000, toMs, "Day after epoch should be 86400000ms")
	})

	t.Run("from is less than to", func(t *testing.T) {
		dates := []time.Time{
			time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2000, 6, 15, 12, 30, 45, 0, time.UTC),
			time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC),
			time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		}

		for _, date := range dates {
			from, to := UUIDRangeForDate(date)

			// Compare UUIDs byte by byte
			isLess := false
			for i := range 16 {
				if from[i] < to[i] {
					isLess = true
					break
				} else if from[i] > to[i] {
					break
				}
			}
			assert.True(t, isLess, "From UUID should be less than To UUID for date %v", date)
		}
	})

	t.Run("sample dates produce expected ranges", func(t *testing.T) {
		tests := []struct {
			name         string
			date         time.Time
			expectedFrom string
			expectedTo   string
		}{
			{
				name:         "2024-01-01",
				date:         time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
				expectedFrom: "018cc251-f400-7000-8000-000000000000",
				expectedTo:   "018cc778-5000-7000-8000-000000000000",
			},
			{
				name:         "2024-06-15",
				date:         time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
				expectedFrom: "01901931-9c00-7000-8000-000000000000",
				expectedTo:   "01901e57-f800-7000-8000-000000000000",
			},
			{
				name:         "2024-12-31",
				date:         time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
				expectedFrom: "01941a03-2000-7000-8000-000000000000",
				expectedTo:   "01941f29-7c00-7000-8000-000000000000",
			},
			{
				name:         "2025-03-15",
				date:         time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC),
				expectedFrom: "01959719-b800-7000-8000-000000000000",
				expectedTo:   "01959c40-1400-7000-8000-000000000000",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				from, to := UUIDRangeForDate(tt.date)

				expectedFrom := uuid.MustParse(tt.expectedFrom)
				expectedTo := uuid.MustParse(tt.expectedTo)

				assert.Equal(t, expectedFrom, from, "From UUID should match expected for %s", tt.name)
				assert.Equal(t, expectedTo, to, "To UUID should match expected for %s", tt.name)
			})
		}
	})
}
