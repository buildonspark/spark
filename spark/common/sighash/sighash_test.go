package sighash

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParse_AcceptsExact32Bytes(t *testing.T) {
	b, err := hex.DecodeString(validHex)
	require.NoError(t, err)

	h, err := Parse(b)
	require.NoError(t, err)
	assert.Equal(t, b, h.Serialize())
}

func TestParse_RejectsWrongLength(t *testing.T) {
	for _, n := range []int{0, 31, 33, 64} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			_, err := Parse(make([]byte, n))
			assert.Error(t, err, "expected error for length %d", n)
		})
	}
}

func TestParseHex_RoundTrip(t *testing.T) {
	h, err := ParseHex(validHex)
	require.NoError(t, err)
	assert.Equal(t, validHex, h.ToHex())
	assert.Equal(t, validHex, h.String())
}

func TestParseHex_RejectsBadHex(t *testing.T) {
	_, err := ParseHex("not-hex")
	require.Error(t, err)
}

func TestMustParseHex_PanicsOnError(t *testing.T) {
	assert.Panics(t, func() { MustParseHex("not-hex") })
}

func TestMustParseHex_OK(t *testing.T) {
	h := MustParseHex(validHex)
	assert.Equal(t, validHex, h.ToHex())
}

func TestSerialize_ZeroReturnsNil(t *testing.T) {
	var h Hash
	assert.Nil(t, h.Serialize())
}

func TestSerialize_ReturnsCopy(t *testing.T) {
	h := MustParseHex(validHex)
	a := h.Serialize()
	a[0] ^= 0xff
	b := h.Serialize()
	assert.NotEqual(t, a, b, "Serialize must return an independent copy")
}

func TestIsZero_Zero(t *testing.T) {
	assert.True(t, Hash{}.IsZero())
}

func TestIsZero_NonZero(t *testing.T) {
	assert.False(t, MustParseHex(validHex).IsZero())
}

func TestEquals(t *testing.T) {
	a := MustParseHex(validHex)
	b := MustParseHex(validHex)
	c := MustParseHex("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	tests := []struct {
		name string
		lhs  Hash
		rhs  Hash
		want bool
	}{
		{name: "same value", lhs: a, rhs: b, want: true},
		{name: "different value", lhs: a, rhs: c, want: false},
		{name: "non-zero vs zero", lhs: a, rhs: Hash{}, want: false},
		{name: "zero vs zero", lhs: Hash{}, rhs: Hash{}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.lhs.Equals(tc.rhs))
		})
	}
}

func TestScan(t *testing.T) {
	rawValid, err := hex.DecodeString(validHex)
	require.NoError(t, err)
	valid := MustParseHex(validHex)

	tests := []struct {
		name string
		src  any
		want Hash
	}{
		{name: "[]byte (Value round-trip)", src: rawValid, want: valid},
		{name: "nil", src: nil, want: Hash{}},
		{name: "nil *sql.Null[[]byte]", src: (*sql.Null[[]byte])(nil), want: Hash{}},
		{name: "valid *sql.Null[[]byte]", src: &sql.Null[[]byte]{V: rawValid, Valid: true}, want: valid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Start non-zero so that the nil/null cases exercise Scan's reset behaviour.
			got := MustParseHex("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
			require.NoError(t, got.Scan(tc.src))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestScan_RejectsWrongType(t *testing.T) {
	var got Hash
	require.Error(t, got.Scan("string-not-bytes"))
}

func TestScan_RejectsWrongLength(t *testing.T) {
	var got Hash
	require.Error(t, got.Scan(make([]byte, 16)))
}

func TestMarshalJSON_ZeroIsNull(t *testing.T) {
	var h Hash
	out, err := json.Marshal(h)
	require.NoError(t, err)
	assert.Equal(t, []byte("null"), out)
}

func TestMarshalJSON_RoundTrip(t *testing.T) {
	h := MustParseHex(validHex)
	out, err := json.Marshal(h)
	require.NoError(t, err)

	var got Hash
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, h, got)
}

func TestUnmarshalJSON_NullSetsZero(t *testing.T) {
	got := MustParseHex(validHex)
	require.NoError(t, json.Unmarshal([]byte("null"), &got))
	assert.Zero(t, got)
}

func TestUnmarshalJSON_RejectsWrongLength(t *testing.T) {
	short, err := json.Marshal(make([]byte, 16)) // Valid JSON, wrong length.
	require.NoError(t, err)
	var got Hash
	assert.Error(t, json.Unmarshal(short, &got))
}

func TestHashIsValueComparable(t *testing.T) {
	a := MustParseHex(validHex)
	b := MustParseHex(validHex)
	assert.Equal(t, a, b)
}

// TestFromTx_Golden pins the BIP-341 taproot sighash to a known vector, guarding against accidental changes in the
// underlying txscript library or in our FromTx wrapper.
func TestFromTx_Golden(t *testing.T) {
	prevTxHex := "020000000001010cb9feccc0bdaac30304e469c50b4420c13c43d466e13813fcf42a73defd3f010000000000ffffffff018038010000000000225120d21e50e12ae122b4a5662c09b67cec7449c8182913bc06761e8b65f0fa2242f701400536f9b7542799f98739eeb6c6adaeb12d7bd418771bc5c6847f2abd19297bd466153600af26ccf0accb605c11ad667c842c5713832af4b7b11f1bcebe57745900000000"
	prevTxBytes, err := hex.DecodeString(prevTxHex)
	require.NoError(t, err)
	prevTx := wire.NewMsgTx(0)
	require.NoError(t, prevTx.Deserialize(bytes.NewReader(prevTxBytes)))

	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: prevTx.TxHash(), Index: 0}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(70_000, prevTx.TxOut[0].PkScript))

	h, err := FromTx(tx, 0, prevTx.TxOut[0])
	require.NoError(t, err)
	assert.Equal(t, "8da5e7aa2b03491d7c2f4359ea4968dd58f69adf9af1a2c6881be0295591c293", h.ToHex())
}
