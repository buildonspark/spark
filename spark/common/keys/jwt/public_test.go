package jwt

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicFromSecp256k1(t *testing.T) {
	rng := &rand.ChaCha8{}
	pub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	k := PublicFromSecp256k1(pub)

	assert.Equal(t, KeyTypeSecp256k1, k.KeyType())
	assert.Equal(t, pub, k.Secp256k1())
	assert.Zero(t, k.P256())
}

func TestPublicFromP256(t *testing.T) {
	pub := keys.GenerateP256PublicKey()

	k := PublicFromP256(pub)

	assert.Equal(t, KeyTypeP256, k.KeyType())
	assert.Equal(t, pub, k.P256())
	assert.Zero(t, k.Secp256k1())
}

func TestPublic_IsZero(t *testing.T) {
	rng := &rand.ChaCha8{}

	tests := []struct {
		name string
		key  Public
		want bool
	}{
		{
			name: "zero value",
			key:  Public{},
			want: true,
		},
		{
			name: "secp256k1",
			key:  PublicFromSecp256k1(keys.MustGeneratePrivateKeyFromRand(rng).Public()),
			want: false,
		},
		{
			name: "P-256",
			key:  PublicFromP256(keys.GenerateP256PublicKey()),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.key.IsZero())
		})
	}
}

func TestPublic_Equals(t *testing.T) {
	rng := &rand.ChaCha8{}
	secpKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	p256Key := keys.GenerateP256PublicKey()

	tests := []struct {
		name string
		a    Public
		b    Public
		want bool
	}{
		{
			name: "same secp256k1",
			a:    PublicFromSecp256k1(secpKey),
			b:    PublicFromSecp256k1(secpKey),
			want: true,
		},
		{
			name: "same P-256",
			a:    PublicFromP256(p256Key),
			b:    PublicFromP256(p256Key),
			want: true,
		},
		{
			name: "different secp256k1",
			a:    PublicFromSecp256k1(secpKey),
			b:    PublicFromSecp256k1(keys.MustGeneratePrivateKeyFromRand(rng).Public()),
			want: false,
		},
		{
			name: "different curves",
			a:    PublicFromSecp256k1(secpKey),
			b:    PublicFromP256(p256Key),
			want: false,
		},
		{
			name: "both zero",
			a:    Public{},
			b:    Public{},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.Equals(tt.b))
			assert.Equal(t, tt.want, tt.b.Equals(tt.a))
		})
	}
}

func TestPublic_Serialize_RoundTrip(t *testing.T) {
	rng := &rand.ChaCha8{}
	tests := []struct {
		name    string
		keyType KeyType
		key     Public
	}{
		{
			name:    "secp256k1",
			keyType: KeyTypeSecp256k1,
			key:     PublicFromSecp256k1(keys.MustGeneratePrivateKeyFromRand(rng).Public()),
		},
		{
			name:    "P-256",
			keyType: KeyTypeP256,
			key:     PublicFromP256(keys.GenerateP256PublicKey()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serialized := tt.key.Serialize()

			assert.Len(t, serialized, 34)
			assert.Equal(t, byte(tt.keyType), serialized[0])

			var decoded Public
			require.NoError(t, decoded.Scan(serialized))
			assert.Equal(t, tt.key.KeyType(), decoded.KeyType())
			assert.Equal(t, tt.key, decoded)
		})
	}
}

func TestPublic_Serialize_Zero(t *testing.T) {
	assert.Nil(t, Public{}.Serialize())
}

func TestPublic_ToHex_String(t *testing.T) {
	rng := &rand.ChaCha8{}
	k := PublicFromSecp256k1(keys.MustGeneratePrivateKeyFromRand(rng).Public())

	hexStr := k.ToHex()
	assert.Equal(t, hexStr, k.String())
	assert.Len(t, hexStr, 68) // 34 bytes = 68 hex chars
}

func TestPublic_Value(t *testing.T) {
	rng := &rand.ChaCha8{}
	k := PublicFromSecp256k1(keys.MustGeneratePrivateKeyFromRand(rng).Public())

	v, err := k.Value()
	require.NoError(t, err)
	assert.Equal(t, k.Serialize(), v)
}

func TestPublic_Scan(t *testing.T) {
	k := PublicFromP256(keys.GenerateP256PublicKey())
	serialized := k.Serialize()

	tests := []struct {
		name  string
		input any
		want  Public
	}{
		{
			name:  "raw bytes",
			input: serialized,
			want:  k,
		},
		{
			name:  "sql.Null valid",
			input: &sql.Null[[]byte]{V: serialized, Valid: true},
			want:  k,
		},
		{
			name:  "nil",
			input: nil,
			want:  Public{},
		},
		{
			name:  "sql.Null invalid",
			input: &sql.Null[[]byte]{Valid: false},
			want:  Public{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dest Public
			require.NoError(t, dest.Scan(tt.input))
			assert.Equal(t, tt.want, dest)
		})
	}
}

func TestPublic_Scan_InvalidInput_Errors(t *testing.T) {
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
			var dest Public
			err := dest.Scan(tt.input)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPublic_MarshalJSON(t *testing.T) {
	rng := &rand.ChaCha8{}
	k := PublicFromSecp256k1(keys.MustGeneratePrivateKeyFromRand(rng).Public())

	tests := []struct {
		name string
		key  Public
		want []byte
	}{
		{
			name: "valid key",
			key:  k,
			want: k.Serialize(),
		},
		{
			name: "zero value",
			key:  Public{},
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

func TestPublic_UnmarshalJSON(t *testing.T) {
	rng := &rand.ChaCha8{}
	k := PublicFromSecp256k1(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	data, err := json.Marshal(k)
	require.NoError(t, err)

	var dest Public
	require.NoError(t, json.Unmarshal(data, &dest))
	assert.Equal(t, k, dest)
}

func TestPublic_UnmarshalJSON_NullInput(t *testing.T) {
	var dest Public
	require.NoError(t, json.Unmarshal([]byte("null"), &dest))
	assert.Zero(t, dest)
}

func TestMustParsePublicHex(t *testing.T) {
	rng := &rand.ChaCha8{}
	k := PublicFromSecp256k1(keys.MustGeneratePrivateKeyFromRand(rng).Public())

	parsed := MustParsePublicHex(hex.EncodeToString(k.Serialize()))
	assert.Equal(t, k, parsed)
}

func TestMustParsePublicHex_InvalidInput_Panics(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "invalid hex", input: "not hex"},
		{name: "valid hex wrong length", input: "deadbeef"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() { MustParsePublicHex(tt.input) })
		})
	}
}
