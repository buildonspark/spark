package common

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUUIDv7FromTime(t *testing.T) {
	t.Run("creates valid UUIDv7 with correct timestamp", func(t *testing.T) {
		now := time.Now()
		u := UUIDv7FromTime(now)

		// Check version is 7
		version := u[6] >> 4
		assert.Equal(t, uint8(7), version, "UUID version should be 7")

		// Check variant is RFC4122 (10xx in binary)
		variant := u[8] >> 6
		assert.Equal(t, uint8(2), variant, "UUID variant should be 2 (RFC4122)")

		// Extract timestamp from UUID and compare
		ms := uint64(u[0])<<40 | uint64(u[1])<<32 | uint64(u[2])<<24 |
			uint64(u[3])<<16 | uint64(u[4])<<8 | uint64(u[5])
		expectedMs := uint64(now.UnixMilli())
		assert.Equal(t, expectedMs, ms, "Extracted timestamp should match input")
	})

	t.Run("creates boundary UUID with zeros for random bits", func(t *testing.T) {
		now := time.Now()
		u := UUIDv7FromTime(now)

		// Check that random bits in byte 6 (lower nibble) are 0
		assert.Equal(t, uint8(0), u[6]&0x0F, "Lower nibble of byte 6 should be 0")

		// Check that byte 7 is 0
		assert.Equal(t, uint8(0), u[7], "Byte 7 should be 0")

		// Check that lower 6 bits of byte 8 are 0
		assert.Equal(t, uint8(0), u[8]&0x3F, "Lower 6 bits of byte 8 should be 0")

		// Check that remaining bytes (9-15) are 0
		for i := 9; i < 16; i++ {
			assert.Equal(t, uint8(0), u[i], "Byte %d should be 0", i)
		}
	})

	t.Run("UUIDs are ordered by time", func(t *testing.T) {
		t1 := time.Now()
		time.Sleep(2 * time.Millisecond)
		t2 := time.Now()

		u1 := UUIDv7FromTime(t1)
		u2 := UUIDv7FromTime(t2)

		// Compare UUIDs as byte slices - u1 should be less than u2
		for i := 0; i < 16; i++ {
			if u1[i] != u2[i] {
				assert.Less(t, u1[i], u2[i], "Earlier UUID should be less than later UUID")
				break
			}
		}
	})

	t.Run("handles past timestamps correctly", func(t *testing.T) {
		past := time.Now().Add(-1 * time.Hour)
		u := UUIDv7FromTime(past)

		// Extract timestamp
		ms := uint64(u[0])<<40 | uint64(u[1])<<32 | uint64(u[2])<<24 |
			uint64(u[3])<<16 | uint64(u[4])<<8 | uint64(u[5])
		expectedMs := uint64(past.UnixMilli())
		assert.Equal(t, expectedMs, ms, "Past timestamp should be encoded correctly")
	})

	t.Run("handles future timestamps correctly", func(t *testing.T) {
		future := time.Now().Add(24 * time.Hour)
		u := UUIDv7FromTime(future)

		// Extract timestamp
		ms := uint64(u[0])<<40 | uint64(u[1])<<32 | uint64(u[2])<<24 |
			uint64(u[3])<<16 | uint64(u[4])<<8 | uint64(u[5])
		expectedMs := uint64(future.UnixMilli())
		assert.Equal(t, expectedMs, ms, "Future timestamp should be encoded correctly")
	})

	t.Run("boundary UUID is less than actual UUIDv7 for same timestamp", func(t *testing.T) {
		now := time.Now()

		// Create multiple real UUIDv7s (they should have random bits set)
		realUUIDs := make([]uuid.UUID, 100)
		for i := 0; i < 100; i++ {
			u, err := uuid.NewV7()
			require.NoError(t, err)
			realUUIDs[i] = u
		}

		// Create boundary UUID for 1 second before now
		boundaryTime := now.Add(-1 * time.Second)
		boundaryUUID := UUIDv7FromTime(boundaryTime)

		// All real UUIDs (created now) should be greater than the boundary (created 1s ago)
		for i, realUUID := range realUUIDs {
			// Compare first 6 bytes (timestamp part)
			for j := 0; j < 6; j++ {
				if boundaryUUID[j] != realUUID[j] {
					assert.Less(t, boundaryUUID[j], realUUID[j],
						"Boundary UUID should be less than real UUID %d at byte %d", i, j)
					break
				}
			}
		}
	})
}
