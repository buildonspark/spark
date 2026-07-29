package uint128

import (
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	result := New()

	assert.True(t, result.IsZero())
	assert.Equal(t, Uint128{lo: 0, hi: 0}, result)
}

func TestFromBytes_SetsCorrectEndianness(t *testing.T) {
	fromUint := Uint128{lo: 0xe8d4a51000}

	h, _ := hex.DecodeString("0000000000000000000000e8d4a51000")
	fromBytes, _ := FromBytes(h)

	require.Equal(t, fromUint, fromBytes)
}

func TestFromUint(t *testing.T) {
	tests := []struct {
		name            string
		input           uint64
		expectedUint128 Uint128
	}{
		{
			name:            "zero",
			input:           0,
			expectedUint128: Uint128{lo: 0, hi: 0},
		},
		{
			name:            "small value",
			input:           42,
			expectedUint128: Uint128{lo: 42, hi: 0},
		},
		{
			name:            "max uint64",
			input:           ^uint64(0),
			expectedUint128: Uint128{lo: ^uint64(0), hi: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FromUint64(tt.input)
			assert.Equal(t, tt.expectedUint128, result)
		})
	}
}

func TestIsZero(t *testing.T) {
	tests := []struct {
		name           string
		u              Uint128
		expectedIsZero bool
	}{
		{
			name:           "zero value",
			u:              Uint128{lo: 0, hi: 0},
			expectedIsZero: true,
		},
		{
			name:           "new",
			u:              New(),
			expectedIsZero: true,
		},
		{
			name:           "non-zero lo",
			u:              Uint128{lo: 1, hi: 0},
			expectedIsZero: false,
		},
		{
			name:           "non-zero hi",
			u:              Uint128{lo: 0, hi: 1},
			expectedIsZero: false,
		},
		{
			name:           "both non-zero",
			u:              Uint128{lo: 1, hi: 1},
			expectedIsZero: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedIsZero, tt.u.IsZero())
		})
	}
}

func TestNewFromBytes(t *testing.T) {
	tests := []struct {
		name            string
		input           []byte
		expectedUint128 Uint128
	}{
		{
			name:            "zero",
			input:           make([]byte, 16),
			expectedUint128: Uint128{lo: 0, hi: 0},
		},
		{
			name: "low value only",
			input: func() []byte {
				b := make([]byte, 16)
				binary.BigEndian.PutUint64(b[8:], 42)
				return b
			}(),
			expectedUint128: Uint128{lo: 42, hi: 0},
		},
		{
			name: "high value only",
			input: func() []byte {
				b := make([]byte, 16)
				binary.BigEndian.PutUint64(b[:8], 100)
				return b
			}(),
			expectedUint128: Uint128{lo: 0, hi: 100},
		},
		{
			name: "both values",
			input: func() []byte {
				b := make([]byte, 16)
				binary.BigEndian.PutUint64(b[8:], 123)
				binary.BigEndian.PutUint64(b[:8], 456)
				return b
			}(),
			expectedUint128: Uint128{lo: 123, hi: 456},
		},
		{
			name: "max value",
			input: func() []byte {
				b := make([]byte, 16)
				binary.BigEndian.PutUint64(b[8:], ^uint64(0))
				binary.BigEndian.PutUint64(b[:8], ^uint64(0))
				return b
			}(),
			expectedUint128: Uint128{lo: ^uint64(0), hi: ^uint64(0)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := FromBytes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedUint128, result)
		})
	}
}

func TestNewFromBytes_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{name: "nil", input: nil},
		{name: "empty", input: []byte{}},
		{name: "too short", input: make([]byte, 15)},
		{name: "too long", input: make([]byte, 17)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromBytes(tt.input)
			require.ErrorContains(t, err, "uint128 must be 16 bytes")
		})
	}
}

func TestBytes(t *testing.T) {
	tests := []struct {
		name string
		u    Uint128
	}{
		{name: "zero", u: Uint128{lo: 0, hi: 0}},
		{name: "low value", u: Uint128{lo: 42, hi: 0}},
		{name: "high value", u: Uint128{lo: 0, hi: 100}},
		{name: "both values", u: Uint128{lo: 123, hi: 456}},
		{name: "max value", u: Uint128{lo: ^uint64(0), hi: ^uint64(0)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bytes := tt.u.Bytes()

			result, err := FromBytes(bytes)
			require.NoError(t, err)
			assert.Equal(t, tt.u, result)

			assert.Equal(t, tt.u.lo, binary.BigEndian.Uint64(bytes[8:]))
			assert.Equal(t, tt.u.hi, binary.BigEndian.Uint64(bytes[:8]))
		})
	}
}

func TestCmp(t *testing.T) {
	tests := []struct {
		name        string
		u           Uint128
		v           Uint128
		expectedCmp int
	}{
		{
			name:        "equal - both zero",
			u:           Uint128{lo: 0, hi: 0},
			v:           Uint128{lo: 0, hi: 0},
			expectedCmp: 0,
		},
		{
			name:        "equal - same values",
			u:           Uint128{lo: 123, hi: 456},
			v:           Uint128{lo: 123, hi: 456},
			expectedCmp: 0,
		},
		{
			name:        "less than - low only",
			u:           Uint128{lo: 100, hi: 0},
			v:           Uint128{lo: 200, hi: 0},
			expectedCmp: -1,
		},
		{
			name:        "less than - high different",
			u:           Uint128{lo: 100, hi: 1},
			v:           Uint128{lo: 50, hi: 2},
			expectedCmp: -1,
		},
		{
			name:        "less than - same high, different low",
			u:           Uint128{lo: 100, hi: 5},
			v:           Uint128{lo: 200, hi: 5},
			expectedCmp: -1,
		},
		{
			name:        "greater than - low only",
			u:           Uint128{lo: 200, hi: 0},
			v:           Uint128{lo: 100, hi: 0},
			expectedCmp: 1,
		},
		{
			name:        "greater than - high different",
			u:           Uint128{lo: 50, hi: 2},
			v:           Uint128{lo: 100, hi: 1},
			expectedCmp: 1,
		},
		{
			name:        "greater than - same high, different low",
			u:           Uint128{lo: 200, hi: 5},
			v:           Uint128{lo: 100, hi: 5},
			expectedCmp: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedCmp, tt.u.Cmp(tt.v))
			assert.Equal(t, -tt.expectedCmp, tt.v.Cmp(tt.u))
		})
	}
}

func TestBigInt(t *testing.T) {
	tests := []struct {
		name           string
		u              Uint128
		expectedBigInt *big.Int
	}{
		{
			name:           "zero",
			u:              Uint128{lo: 0, hi: 0},
			expectedBigInt: big.NewInt(0),
		},
		{
			name:           "low value only",
			u:              Uint128{lo: 42, hi: 0},
			expectedBigInt: big.NewInt(42),
		},
		{
			name:           "high value only",
			u:              Uint128{lo: 0, hi: 1},
			expectedBigInt: new(big.Int).Lsh(big.NewInt(1), 64),
		},
		{
			name:           "both values",
			u:              Uint128{lo: 100, hi: 1},
			expectedBigInt: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(100)),
		},
		{
			name:           "max uint64 in low",
			u:              Uint128{lo: ^uint64(0), hi: 0},
			expectedBigInt: new(big.Int).SetUint64(^uint64(0)),
		},
		{
			name:           "max value",
			u:              Uint128{lo: ^uint64(0), hi: ^uint64(0)},
			expectedBigInt: new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.u.BigInt()
			assert.Equal(t, tt.expectedBigInt, result)
		})
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		name           string
		u              Uint128
		expectedString string
	}{
		{
			name:           "zero",
			u:              Uint128{lo: 0, hi: 0},
			expectedString: "0",
		},
		{
			name:           "small value",
			u:              Uint128{lo: 42, hi: 0},
			expectedString: "42",
		},
		{
			name:           "max uint64",
			u:              Uint128{lo: ^uint64(0), hi: 0},
			expectedString: "18446744073709551615",
		},
		{
			name:           "high value only",
			u:              Uint128{lo: 0, hi: 1},
			expectedString: "18446744073709551616", // 2^64
		},
		{
			name:           "both values",
			u:              Uint128{lo: 123, hi: 456},
			expectedString: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(456), 64), big.NewInt(123)).String(),
		},
		{
			name:           "max value",
			u:              Uint128{lo: ^uint64(0), hi: ^uint64(0)},
			expectedString: "340282366920938463463374607431768211455", // 2^128-1
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.u.String()

			assert.Equal(t, tt.expectedString, result)
			assert.Equal(t, tt.u.BigInt().String(), result)
		})
	}
}

func TestValue(t *testing.T) {
	tests := []struct {
		name          string
		u             Uint128
		expectedValue string
	}{
		{
			name:          "zero",
			u:             Uint128{lo: 0, hi: 0},
			expectedValue: "0",
		},
		{
			name:          "small value",
			u:             Uint128{lo: 42, hi: 0},
			expectedValue: "42",
		},
		{
			name:          "max value",
			u:             Uint128{lo: ^uint64(0), hi: ^uint64(0)},
			expectedValue: "340282366920938463463374607431768211455",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := tt.u.Value()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedValue, value)
		})
	}
}

func TestScan(t *testing.T) {
	tests := []struct {
		name            string
		input           any
		expectedUint128 Uint128
	}{
		{
			name:            "nil",
			input:           nil,
			expectedUint128: Uint128{lo: 0, hi: 0},
		},
		{
			name:            "string - zero",
			input:           "0",
			expectedUint128: Uint128{lo: 0, hi: 0},
		},
		{
			name:            "string - small value",
			input:           "42",
			expectedUint128: Uint128{lo: 42, hi: 0},
		},
		{
			name:            "string - max uint64",
			input:           "18446744073709551615",
			expectedUint128: Uint128{lo: ^uint64(0), hi: 0},
		},
		{
			name:            "string - larger than uint64",
			input:           "18446744073709551616", // 2^64
			expectedUint128: Uint128{lo: 0, hi: 1},
		},
		{
			name:            "string - max uint128",
			input:           "340282366920938463463374607431768211455",
			expectedUint128: Uint128{lo: ^uint64(0), hi: ^uint64(0)},
		},
		{
			name:            "bytes - zero",
			input:           []byte("0"),
			expectedUint128: Uint128{lo: 0, hi: 0},
		},
		{
			name:            "bytes - value",
			input:           []byte("12345"),
			expectedUint128: Uint128{lo: 12345, hi: 0},
		},
		{
			name:            "sql.Null - valid",
			input:           &sql.Null[[]byte]{V: []byte("42"), Valid: true},
			expectedUint128: Uint128{lo: 42, hi: 0},
		},
		{
			name:            "sql.Null - null",
			input:           &sql.Null[[]byte]{Valid: false},
			expectedUint128: Uint128{lo: 0, hi: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &Uint128{}
			err := dest.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedUint128, *dest)
		})
	}
}

func TestScan_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expectedErr string
	}{
		{
			name:        "invalid string",
			input:       "not a number",
			expectedErr: "invalid numeric when scanning",
		},
		{
			name:        "negative value",
			input:       "-1",
			expectedErr: "uint128 out of range",
		},
		{
			name:        "too large",
			input:       "340282366920938463463374607431768211456", // 2^128
			expectedErr: "uint128 out of range",
		},
		{
			name:        "unsupported type",
			input:       123,
			expectedErr: "unsupported src",
		},
		{
			name:        "bytes - invalid",
			input:       []byte("invalid"),
			expectedErr: "invalid numeric when scanning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &Uint128{}
			err := dest.Scan(tt.input)

			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}

func TestScanValue_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		u    Uint128
	}{
		{name: "zero", u: Uint128{lo: 0, hi: 0}},
		{name: "small value", u: Uint128{lo: 42, hi: 0}},
		{name: "max value", u: Uint128{lo: ^uint64(0), hi: ^uint64(0)}},
		{name: "both values", u: Uint128{lo: 123, hi: 456}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, _ := tt.u.Value()

			dest := Uint128{}
			err := dest.Scan(value)
			require.NoError(t, err)

			assert.Equal(t, tt.u, dest)
		})
	}
}
