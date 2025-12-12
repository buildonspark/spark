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
		name  string
		input uint64
		want  Uint128
	}{
		{
			name:  "zero",
			input: 0,
			want:  Uint128{lo: 0, hi: 0},
		},
		{
			name:  "small value",
			input: 42,
			want:  Uint128{lo: 42, hi: 0},
		},
		{
			name:  "max uint64",
			input: ^uint64(0),
			want:  Uint128{lo: ^uint64(0), hi: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FromUint64(tt.input)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestIsZero(t *testing.T) {
	tests := []struct {
		name string
		u    Uint128
		want bool
	}{
		{
			name: "zero value",
			u:    Uint128{lo: 0, hi: 0},
			want: true,
		},
		{
			name: "new",
			u:    New(),
			want: true,
		},
		{
			name: "non-zero lo",
			u:    Uint128{lo: 1, hi: 0},
			want: false,
		},
		{
			name: "non-zero hi",
			u:    Uint128{lo: 0, hi: 1},
			want: false,
		},
		{
			name: "both non-zero",
			u:    Uint128{lo: 1, hi: 1},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.u.IsZero())
		})
	}
}

func TestNewFromBytes(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  Uint128
	}{
		{
			name:  "zero",
			input: make([]byte, 16),
			want:  Uint128{lo: 0, hi: 0},
		},
		{
			name: "low value only",
			input: func() []byte {
				b := make([]byte, 16)
				binary.BigEndian.PutUint64(b[8:], 42)
				return b
			}(),
			want: Uint128{lo: 42, hi: 0},
		},
		{
			name: "high value only",
			input: func() []byte {
				b := make([]byte, 16)
				binary.BigEndian.PutUint64(b[:8], 100)
				return b
			}(),
			want: Uint128{lo: 0, hi: 100},
		},
		{
			name: "both values",
			input: func() []byte {
				b := make([]byte, 16)
				binary.BigEndian.PutUint64(b[8:], 123)
				binary.BigEndian.PutUint64(b[:8], 456)
				return b
			}(),
			want: Uint128{lo: 123, hi: 456},
		},
		{
			name: "max value",
			input: func() []byte {
				b := make([]byte, 16)
				binary.BigEndian.PutUint64(b[8:], ^uint64(0))
				binary.BigEndian.PutUint64(b[:8], ^uint64(0))
				return b
			}(),
			want: Uint128{lo: ^uint64(0), hi: ^uint64(0)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := FromBytes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, result)
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
		name string
		u    Uint128
		v    Uint128
		want int
	}{
		{
			name: "equal - both zero",
			u:    Uint128{lo: 0, hi: 0},
			v:    Uint128{lo: 0, hi: 0},
			want: 0,
		},
		{
			name: "equal - same values",
			u:    Uint128{lo: 123, hi: 456},
			v:    Uint128{lo: 123, hi: 456},
			want: 0,
		},
		{
			name: "less than - low only",
			u:    Uint128{lo: 100, hi: 0},
			v:    Uint128{lo: 200, hi: 0},
			want: -1,
		},
		{
			name: "less than - high different",
			u:    Uint128{lo: 100, hi: 1},
			v:    Uint128{lo: 50, hi: 2},
			want: -1,
		},
		{
			name: "less than - same high, different low",
			u:    Uint128{lo: 100, hi: 5},
			v:    Uint128{lo: 200, hi: 5},
			want: -1,
		},
		{
			name: "greater than - low only",
			u:    Uint128{lo: 200, hi: 0},
			v:    Uint128{lo: 100, hi: 0},
			want: 1,
		},
		{
			name: "greater than - high different",
			u:    Uint128{lo: 50, hi: 2},
			v:    Uint128{lo: 100, hi: 1},
			want: 1,
		},
		{
			name: "greater than - same high, different low",
			u:    Uint128{lo: 200, hi: 5},
			v:    Uint128{lo: 100, hi: 5},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.u.Cmp(tt.v))
			assert.Equal(t, -tt.want, tt.v.Cmp(tt.u))
		})
	}
}

func TestBigInt(t *testing.T) {
	tests := []struct {
		name string
		u    Uint128
		want *big.Int
	}{
		{
			name: "zero",
			u:    Uint128{lo: 0, hi: 0},
			want: big.NewInt(0),
		},
		{
			name: "low value only",
			u:    Uint128{lo: 42, hi: 0},
			want: big.NewInt(42),
		},
		{
			name: "high value only",
			u:    Uint128{lo: 0, hi: 1},
			want: new(big.Int).Lsh(big.NewInt(1), 64),
		},
		{
			name: "both values",
			u:    Uint128{lo: 100, hi: 1},
			want: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(100)),
		},
		{
			name: "max uint64 in low",
			u:    Uint128{lo: ^uint64(0), hi: 0},
			want: new(big.Int).SetUint64(^uint64(0)),
		},
		{
			name: "max value",
			u:    Uint128{lo: ^uint64(0), hi: ^uint64(0)},
			want: new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.u.BigInt()
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		name string
		u    Uint128
		want string
	}{
		{
			name: "zero",
			u:    Uint128{lo: 0, hi: 0},
			want: "0",
		},
		{
			name: "small value",
			u:    Uint128{lo: 42, hi: 0},
			want: "42",
		},
		{
			name: "max uint64",
			u:    Uint128{lo: ^uint64(0), hi: 0},
			want: "18446744073709551615",
		},
		{
			name: "high value only",
			u:    Uint128{lo: 0, hi: 1},
			want: "18446744073709551616", // 2^64
		},
		{
			name: "both values",
			u:    Uint128{lo: 123, hi: 456},
			want: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(456), 64), big.NewInt(123)).String(),
		},
		{
			name: "max value",
			u:    Uint128{lo: ^uint64(0), hi: ^uint64(0)},
			want: "340282366920938463463374607431768211455", // 2^128-1
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.u.String()

			assert.Equal(t, tt.want, result)
			assert.Equal(t, tt.u.BigInt().String(), result)
		})
	}
}

func TestValue(t *testing.T) {
	tests := []struct {
		name string
		u    Uint128
		want string
	}{
		{
			name: "zero",
			u:    Uint128{lo: 0, hi: 0},
			want: "0",
		},
		{
			name: "small value",
			u:    Uint128{lo: 42, hi: 0},
			want: "42",
		},
		{
			name: "max value",
			u:    Uint128{lo: ^uint64(0), hi: ^uint64(0)},
			want: "340282366920938463463374607431768211455",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := tt.u.Value()
			require.NoError(t, err)
			assert.Equal(t, tt.want, value)
		})
	}
}

func TestScan(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  Uint128
	}{
		{
			name:  "nil",
			input: nil,
			want:  Uint128{lo: 0, hi: 0},
		},
		{
			name:  "string - zero",
			input: "0",
			want:  Uint128{lo: 0, hi: 0},
		},
		{
			name:  "string - small value",
			input: "42",
			want:  Uint128{lo: 42, hi: 0},
		},
		{
			name:  "string - max uint64",
			input: "18446744073709551615",
			want:  Uint128{lo: ^uint64(0), hi: 0},
		},
		{
			name:  "string - larger than uint64",
			input: "18446744073709551616", // 2^64
			want:  Uint128{lo: 0, hi: 1},
		},
		{
			name:  "string - max uint128",
			input: "340282366920938463463374607431768211455",
			want:  Uint128{lo: ^uint64(0), hi: ^uint64(0)},
		},
		{
			name:  "bytes - zero",
			input: []byte("0"),
			want:  Uint128{lo: 0, hi: 0},
		},
		{
			name:  "bytes - value",
			input: []byte("12345"),
			want:  Uint128{lo: 12345, hi: 0},
		},
		{
			name:  "sql.Null - valid",
			input: &sql.Null[[]byte]{V: []byte("42"), Valid: true},
			want:  Uint128{lo: 42, hi: 0},
		},
		{
			name:  "sql.Null - null",
			input: &sql.Null[[]byte]{Valid: false},
			want:  Uint128{lo: 0, hi: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &Uint128{}
			err := dest.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.want, *dest)
		})
	}
}

func TestScan_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		wantErr string
	}{
		{
			name:    "invalid string",
			input:   "not a number",
			wantErr: "invalid numeric when scanning",
		},
		{
			name:    "negative value",
			input:   "-1",
			wantErr: "uint128 out of range",
		},
		{
			name:    "too large",
			input:   "340282366920938463463374607431768211456", // 2^128
			wantErr: "uint128 out of range",
		},
		{
			name:    "unsupported type",
			input:   123,
			wantErr: "unsupported src",
		},
		{
			name:    "bytes - invalid",
			input:   []byte("invalid"),
			wantErr: "invalid numeric when scanning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &Uint128{}
			err := dest.Scan(tt.input)

			require.ErrorContains(t, err, tt.wantErr)
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
