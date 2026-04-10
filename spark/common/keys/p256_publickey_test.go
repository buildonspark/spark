package keys

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseP256PublicKey(t *testing.T) {
	key := GenerateP256PublicKey()
	serialized := key.Serialize()

	result, err := ParseP256PublicKey(serialized)

	require.NoError(t, err)
	assert.Equal(t, serialized, result.Serialize())
}

func TestParseP256PublicKey_Uncompressed(t *testing.T) {
	key := GenerateP256PublicKey()
	uncompressed, _ := key.ToECDSA().Bytes()

	result, err := ParseP256PublicKey(uncompressed)
	require.NoError(t, err)

	assert.Equal(t, key, result)
}

func TestParseP256PublicKey_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantErr string
	}{
		{
			name:    "nil",
			input:   nil,
			wantErr: "malformed public key: invalid length: 0",
		},
		{
			name:    "empty",
			input:   []byte{},
			wantErr: "malformed public key: invalid length: 0",
		},
		{
			name:    "too short",
			input:   bytes.Repeat([]byte{1}, 3),
			wantErr: "malformed public key: invalid length: 3",
		},
		{
			name:    "wrong length",
			input:   bytes.Repeat([]byte{1}, 34),
			wantErr: "malformed public key: invalid length: 34",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseP256PublicKey(tt.input)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestParseP256PublicKeyHex(t *testing.T) {
	key := GenerateP256PublicKey()

	result, err := ParseP256PublicKeyHex(key.ToHex())

	require.NoError(t, err)
	assert.Equal(t, key, result)
}

func TestMustParseP256PublicKeyHex(t *testing.T) {
	key := GenerateP256PublicKey()

	parsed := MustParseP256PublicKeyHex(key.ToHex())

	assert.Equal(t, key, parsed)
}

func TestMustParseP256PublicKeyHex_InvalidInput_Panics(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "invalid hex", input: "not hex"},
		{name: "valid hex wrong length", input: "deadbeef"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() { MustParseP256PublicKeyHex(tt.input) })
		})
	}
}

func TestP256Public_Equals(t *testing.T) {
	key1 := GenerateP256PublicKey()
	key2 := GenerateP256PublicKey()

	tests := []struct {
		name string
		a    P256Public
		b    P256Public
		want bool
	}{
		{
			name: "same keys",
			a:    key1,
			b:    key1,
			want: true,
		},
		{
			name: "different keys",
			a:    key1,
			b:    key2,
			want: false,
		},
		{
			name: "both zero",
			a:    P256Public{},
			b:    P256Public{},
			want: true,
		},
		{
			name: "left zero",
			a:    P256Public{},
			b:    key1,
			want: false,
		},
		{
			name: "right zero",
			a:    key1,
			b:    P256Public{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.Equals(tt.b))
			assert.Equal(t, tt.want, tt.b.Equals(tt.a))
		})
	}
}

func TestP256Public_ToHex(t *testing.T) {
	key := GenerateP256PublicKey()

	hexStr := key.ToHex()

	decoded, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	assert.Equal(t, key.Serialize(), decoded)
}

func TestP256Public_String(t *testing.T) {
	key := GenerateP256PublicKey()
	assert.Equal(t, key.ToHex(), key.String())
}

func TestP256Public_IsZero(t *testing.T) {
	tests := []struct {
		name     string
		key      P256Public
		wantZero bool
	}{
		{
			name:     "zero value",
			key:      P256Public{},
			wantZero: true,
		},
		{
			name:     "generated key",
			key:      GenerateP256PublicKey(),
			wantZero: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantZero, tt.key.IsZero())
		})
	}
}

func TestP256Public_Serialize_Empty_ReturnsEmpty(t *testing.T) {
	assert.Empty(t, P256Public{}.Serialize())
}

func TestP256Public_Value(t *testing.T) {
	key := GenerateP256PublicKey()

	value, err := key.Value()
	require.NoError(t, err)

	assert.Equal(t, key.Serialize(), value)
}

func TestP256Public_Scan(t *testing.T) {
	key := GenerateP256PublicKey()

	tests := []struct {
		name  string
		input any
		want  P256Public
	}{
		{
			name:  "valid key",
			input: &sql.Null[[]byte]{V: key.Serialize(), Valid: true},
			want:  key,
		},
		{
			name:  "nil value",
			input: nil,
			want:  P256Public{},
		},
		{
			name:  "nil sql.Null",
			input: (*sql.Null[[]byte])(nil),
			want:  P256Public{},
		},
		{
			name:  "null value",
			input: &sql.Null[[]byte]{Valid: false},
			want:  P256Public{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dest P256Public
			err := dest.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.want, dest)
		})
	}
}

func TestP256Public_Scan_InvalidInput_Errors(t *testing.T) {
	var dest P256Public
	err := dest.Scan("not bytes")
	require.ErrorContains(t, err, "unexpected input for Scan")
}

func TestP256Public_MarshalJSON(t *testing.T) {
	key := GenerateP256PublicKey()

	tests := []struct {
		name string
		key  P256Public
		want []byte
	}{
		{
			name: "valid key",
			key:  key,
			want: key.Serialize(),
		},
		{
			name: "empty key",
			key:  P256Public{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.key.MarshalJSON()
			require.NoError(t, err)

			var unmarshaled []byte
			require.NoError(t, json.Unmarshal(data, &unmarshaled))
			assert.Equal(t, tt.want, unmarshaled)
		})
	}
}

func TestP256Public_UnmarshalJSON(t *testing.T) {
	key := GenerateP256PublicKey()
	data, err := json.Marshal(key)
	require.NoError(t, err)

	var dest P256Public
	require.NoError(t, json.Unmarshal(data, &dest))
	assert.Equal(t, key, dest)
}

func TestP256Public_UnmarshalJSON_InvalidInput_Errors(t *testing.T) {
	var dest P256Public
	err := json.Unmarshal([]byte(`"invalid hex"`), &dest)
	require.Error(t, err)
	assert.Zero(t, dest)
}

func TestP256PublicFromECDSA(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	pub, err := P256PublicFromECDSA(&priv.PublicKey)
	require.NoError(t, err)

	// Round-trip: the ECDSA key we get back should have the same coordinates.
	got := pub.ToECDSA()
	assert.Equal(t, priv.PublicKey.X, got.X)
	assert.Equal(t, priv.PublicKey.Y, got.Y)
}

func TestP256PublicFromECDSA_WrongCurve(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)

	_, err = P256PublicFromECDSA(&priv.PublicKey)
	assert.ErrorContains(t, err, "expected P-256 curve")
}
