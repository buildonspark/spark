package common

import (
	"testing"
)

func TestNewBitMap(t *testing.T) {
	size := 10
	bm := NewBitMap(size)
	if len(bm.value) != (size+7)/8 {
		t.Errorf("Expected BitMap size to be %d, got %d", (size+7)/8, len(bm.value))
	}
}

func TestBitMap_SetAndGet(t *testing.T) {
	bm := NewBitMap(16) // 16 bits

	// Test setting and getting individual bits
	testCases := []struct {
		index int
		value bool
	}{
		{0, true},
		{1, false},
		{7, true},
		{8, true},
		{15, true},
	}

	for _, tc := range testCases {
		bm.Set(tc.index, tc.value)
		if got := bm.Get(tc.index); got != tc.value {
			t.Errorf("BitMap.Get(%d) = %v, want %v", tc.index, got, tc.value)
		}
	}
}

func TestBitMap_MultipleOperations(t *testing.T) {
	bm := NewBitMap(16) // 16 bits

	// Set multiple bits
	bm.Set(0, true)
	bm.Set(1, true)
	bm.Set(2, true)

	// Verify all bits are set correctly
	if !bm.Get(0) || !bm.Get(1) || !bm.Get(2) {
		t.Error("Expected bits 0, 1, and 2 to be set to true")
	}

	// Clear some bits
	bm.Set(1, false)
	if bm.Get(1) {
		t.Error("Expected bit 1 to be false after clearing")
	}

	// Set bits across byte boundaries
	bm.Set(7, true)
	bm.Set(8, true)
	if !bm.Get(7) || !bm.Get(8) {
		t.Error("Expected bits 7 and 8 to be set correctly across byte boundary")
	}
}

func TestBitMap_EdgeCases(t *testing.T) {
	bm := NewBitMap(8) // 8 bits

	// Test setting and getting the last bit
	bm.Set(7, true)
	if !bm.Get(7) {
		t.Error("Expected last bit to be set to true")
	}

	// Test setting and getting the first bit
	bm.Set(0, true)
	if !bm.Get(0) {
		t.Error("Expected first bit to be set to true")
	}

	// Test toggling bits
	bm.Set(3, true)
	bm.Set(3, false)
	if bm.Get(3) {
		t.Error("Expected bit 3 to be false after toggling")
	}
}

func TestBitMap_IsAllSet(t *testing.T) {
	// Test with 8 bits (1 byte)
	bm := NewBitMap(8)

	// Initially should not be all set
	if bm.IsAllSet() {
		t.Error("Expected IsAllSet to return false for newly created BitMap")
	}

	// Set all bits
	for i := range 8 {
		bm.Set(i, true)
	}
	if !bm.IsAllSet() {
		t.Error("Expected IsAllSet to return true when all bits are set")
	}

	// Clear one bit
	bm.Set(3, false)
	if bm.IsAllSet() {
		t.Error("Expected IsAllSet to return false when one bit is cleared")
	}

	// Test with multiple bytes (16 bits)
	bm2 := NewBitMap(16)

	// Set all bits
	for i := range 16 {
		bm2.Set(i, true)
	}
	if !bm2.IsAllSet() {
		t.Error("Expected IsAllSet to return true when all bits are set in multiple bytes")
	}

	// Clear one bit in second byte
	bm2.Set(10, false)
	if bm2.IsAllSet() {
		t.Error("Expected IsAllSet to return false when one bit is cleared in second byte")
	}

	// Test with 2 bits
	bm3 := NewBitMap(2)

	// Initially should not be all set
	if bm3.IsAllSet() {
		t.Error("Expected IsAllSet to return false for newly created BitMap with 2 bits")
	}

	// Set first bit
	bm3.Set(0, true)
	if bm3.IsAllSet() {
		t.Error("Expected IsAllSet to return false when only first bit is set")
	}

	// Set second bit
	bm3.Set(1, true)
	if !bm3.IsAllSet() {
		t.Error("Expected IsAllSet to return true when both bits are set")
	}

	// Clear first bit
	bm3.Set(0, false)
	if bm3.IsAllSet() {
		t.Error("Expected IsAllSet to return false when only second bit is set")
	}
}
