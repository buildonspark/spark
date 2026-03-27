package keys

import (
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

func mustGenerateP256Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return priv
}

func TestParseJwtPubKeySecp256k1_Compressed(t *testing.T) {
	compressed := MustGeneratePrivateKeyFromRand(rng).Public().Serialize()

	k, err := ParseJwtPubKeySecp256k1(compressed)

	require.NoError(t, err)
	assert.Equal(t, JwtKeyTypeSecp256k1, k.KeyType())
	assert.False(t, k.IsZero())
	assert.NotNil(t, k.ToECDSA())
}

func TestParseJwtPubKeySecp256k1_Uncompressed(t *testing.T) {
	priv := MustGeneratePrivateKeyFromRand(rng)
	pub := priv.ToBTCEC().PubKey()
	uncompressed := pub.SerializeUncompressed()

	k, err := ParseJwtPubKeySecp256k1(uncompressed)

	require.NoError(t, err)
	assert.Equal(t, JwtKeyTypeSecp256k1, k.KeyType())
	// Round-trip: serialized form should be the compressed key.
	serialized := k.Serialize()
	assert.Equal(t, byte(JwtKeyTypeSecp256k1), serialized[0])
	assert.Equal(t, pub.SerializeCompressed(), serialized[1:])
}

func TestParseJwtPubKeySecp256k1_Invalid(t *testing.T) {
	_, err := ParseJwtPubKeySecp256k1([]byte{0x00, 0x01, 0x02})
	assert.ErrorContains(t, err, "invalid secp256k1 key")
}

func TestParseJwtPubKeyP256_Compressed(t *testing.T) {
	priv := mustGenerateP256Key(t)
	compressed := elliptic.MarshalCompressed(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)

	k, err := ParseJwtPubKeyP256(compressed)

	require.NoError(t, err)
	assert.Equal(t, JwtKeyTypeP256, k.KeyType())
	assert.False(t, k.IsZero())
}

func TestParseJwtPubKeyP256_Uncompressed(t *testing.T) {
	priv := mustGenerateP256Key(t)
	uncompressed := elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)

	k, err := ParseJwtPubKeyP256(uncompressed)

	require.NoError(t, err)
	assert.Equal(t, JwtKeyTypeP256, k.KeyType())
	// Round-trip: serialized form should be the compressed key.
	serialized := k.Serialize()
	assert.Equal(t, byte(JwtKeyTypeP256), serialized[0])
	assert.Equal(t, elliptic.MarshalCompressed(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y), serialized[1:])
}

func TestParseJwtPubKeyP256_Invalid(t *testing.T) {
	_, err := ParseJwtPubKeyP256([]byte{0x00, 0x01, 0x02})
	assert.ErrorContains(t, err, "invalid P-256 key")
}

func TestJwtPubKey_Serialize_RoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		keyType JwtKeyType
		makeKey func(t *testing.T) JwtPubKey
	}{
		{
			name:    "secp256k1",
			keyType: JwtKeyTypeSecp256k1,
			makeKey: func(t *testing.T) JwtPubKey {
				compressed := MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
				k, err := ParseJwtPubKeySecp256k1(compressed)
				require.NoError(t, err)
				return k
			},
		},
		{
			name:    "P-256",
			keyType: JwtKeyTypeP256,
			makeKey: func(t *testing.T) JwtPubKey {
				priv := mustGenerateP256Key(t)
				compressed := elliptic.MarshalCompressed(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)
				k, err := ParseJwtPubKeyP256(compressed)
				require.NoError(t, err)
				return k
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := tt.makeKey(t)
			serialized := original.Serialize()

			assert.Len(t, serialized, 34)
			assert.Equal(t, byte(tt.keyType), serialized[0])

			var decoded JwtPubKey
			require.NoError(t, decoded.Scan(serialized))
			assert.Equal(t, original.KeyType(), decoded.KeyType())
			assert.Equal(t, original.ToECDSA(), decoded.ToECDSA())
		})
	}
}

func TestJwtPubKey_Scan(t *testing.T) {
	priv := mustGenerateP256Key(t)
	compressed := elliptic.MarshalCompressed(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)
	k, err := ParseJwtPubKeyP256(compressed)
	require.NoError(t, err)
	serialized := k.Serialize()

	tests := []struct {
		name    string
		input   any
		wantNil bool
	}{
		{
			name:  "raw bytes",
			input: serialized,
		},
		{
			name:  "sql.Null valid",
			input: &sql.Null[[]byte]{V: serialized, Valid: true},
		},
		{
			name:    "nil",
			input:   nil,
			wantNil: true,
		},
		{
			name:    "sql.Null invalid",
			input:   &sql.Null[[]byte]{Valid: false},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dest JwtPubKey
			require.NoError(t, dest.Scan(tt.input))
			if tt.wantNil {
				assert.True(t, dest.IsZero())
			} else {
				assert.Equal(t, JwtKeyTypeP256, dest.KeyType())
				assert.Equal(t, k.ToECDSA(), dest.ToECDSA())
			}
		})
	}
}

func TestJwtPubKey_Scan_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		wantErr string
	}{
		{
			name:    "wrong type",
			input:   "not bytes",
			wantErr: "unexpected input for Scan",
		},
		{
			name:    "wrong length",
			input:   []byte{0x01, 0x02, 0x03},
			wantErr: "expected 34 bytes",
		},
		{
			name:    "unknown discriminator",
			input:   append([]byte{0xFF}, make([]byte, 33)...),
			wantErr: "unknown curve discriminator 0xff",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dest JwtPubKey
			err := dest.Scan(tt.input)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestJwtPubKey_Value(t *testing.T) {
	compressed := MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	k, err := ParseJwtPubKeySecp256k1(compressed)
	require.NoError(t, err)

	v, err := k.Value()
	require.NoError(t, err)
	assert.Equal(t, k.Serialize(), v)
}

func TestJwtPubKey_IsZero(t *testing.T) {
	assert.True(t, JwtPubKey{}.IsZero())

	compressed := MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	k, err := ParseJwtPubKeySecp256k1(compressed)
	require.NoError(t, err)
	assert.False(t, k.IsZero())
}

func TestJwtPubKey_MarshalJSON(t *testing.T) {
	compressed := MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	k, err := ParseJwtPubKeySecp256k1(compressed)
	require.NoError(t, err)

	tests := []struct {
		name string
		key  JwtPubKey
		want []byte
	}{
		{name: "valid key", key: k, want: k.Serialize()},
		{name: "zero value", key: JwtPubKey{}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.key.MarshalJSON()
			require.NoError(t, err)

			var got []byte
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestJwtPubKey_UnmarshalJSON(t *testing.T) {
	compressed := MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	k, err := ParseJwtPubKeySecp256k1(compressed)
	require.NoError(t, err)

	data, err := json.Marshal(k)
	require.NoError(t, err)

	var dest JwtPubKey
	require.NoError(t, json.Unmarshal(data, &dest))
	assert.Equal(t, k.KeyType(), dest.KeyType())
	assert.Equal(t, k.ToECDSA(), dest.ToECDSA())
}

func TestJwtPubKey_UnmarshalJSON_NullInput(t *testing.T) {
	var dest JwtPubKey
	require.NoError(t, json.Unmarshal([]byte("null"), &dest))
	assert.True(t, dest.IsZero())
}

func TestMustParseJwtPubKeyHex_Valid(t *testing.T) {
	compressed := MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	k, err := ParseJwtPubKeySecp256k1(compressed)
	require.NoError(t, err)

	parsed := MustParseJwtPubKeyHex(hex.EncodeToString(k.Serialize()))
	assert.Equal(t, k.KeyType(), parsed.KeyType())
	assert.Equal(t, k.ToECDSA(), parsed.ToECDSA())
}

func TestMustParseJwtPubKeyHex_InvalidHex_Panics(t *testing.T) {
	assert.Panics(t, func() { MustParseJwtPubKeyHex("not hex") })
}

func TestMustParseJwtPubKeyHex_InvalidKey_Panics(t *testing.T) {
	assert.Panics(t, func() { MustParseJwtPubKeyHex("deadbeef") }) // valid hex, wrong length
}
