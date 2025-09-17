package keys

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func TestGeneratePrivateKey(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	assert.NotNil(t, privKey)
	assert.Len(t, privKey.Serialize(), 32)
}

func TestParsePrivateKey(t *testing.T) {
	privKeyBytes := MustGeneratePrivateKeyFromRand(rng).Serialize()

	result, err := ParsePrivateKey(privKeyBytes)

	require.NoError(t, err)
	assert.Equal(t, privKeyBytes, result.Serialize())
}

func TestParsePrivateKey_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantErr string
	}{
		{
			name:    "nil",
			input:   nil,
			wantErr: "private key must be 32 bytes",
		},
		{
			name:    "empty",
			input:   []byte{},
			wantErr: "private key must be 32 bytes",
		},
		{
			name:    "too short",
			input:   bytes.Repeat([]byte{1}, 31),
			wantErr: "private key must be 32 bytes",
		},
		{
			name:    "too long",
			input:   bytes.Repeat([]byte{1}, 33),
			wantErr: "private key must be 32 bytes",
		},
		{
			name:    "zero key",
			input:   bytes.Repeat([]byte{0}, 32),
			wantErr: "private key must not be zero",
		},
		{
			name:    "zero (mod curve order) key",
			input:   secp256k1.S256().N.Bytes(),
			wantErr: "private key must not be zero",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePrivateKey(tt.input)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPrivateKeyFromBigInt(t *testing.T) {
	privKey := MustGeneratePrivateKeyFromRand(rng)
	privKeyInt := new(big.Int).SetBytes(privKey.Serialize())

	result, err := PrivateKeyFromBigInt(privKeyInt)
	require.NoError(t, err)
	assert.Equal(t, privKey.Serialize(), result.Serialize())
}

func TestPrivateKeyFromBigInt_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   *big.Int
		wantErr string
	}{
		{
			name:    "zero",
			input:   big.NewInt(0),
			wantErr: "private key must not be zero",
		},
		{
			name:    "too large",
			input:   new(big.Int).Lsh(big.NewInt(1), 257), // 257 bits
			wantErr: "private key must not be represented by an Int larger than 32 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrivateKeyFromBigInt(tt.input)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPrivate_Public(t *testing.T) {
	privKey := MustGeneratePrivateKeyFromRand(rng)
	pubKey := privKey.Public()

	// Verify that the public key matches the private key
	assert.Equal(t, *privKey.key.PubKey(), pubKey.key)
}

func TestPrivate_Add(t *testing.T) {
	privA := MustGeneratePrivateKeyFromRand(rng)
	privB := MustGeneratePrivateKeyFromRand(rng)

	got := privA.Add(privB)

	// Verify that the sum equals the private key of (privA + privB)
	wantPubKey := privA.Public().Add(privB.Public())
	gotPubKey := got.Public()
	assert.Equal(t, wantPubKey, gotPubKey)
}

func TestPrivate_Add_Overflow(t *testing.T) {
	privA := MustGeneratePrivateKeyFromRand(rng)
	// N is the order of the curve, so if we're not computing the result mod N, it won't equal privA
	mustOverflow, err := PrivateKeyFromBigInt(secp256k1.S256().N)
	require.NoError(t, err)

	got := privA.Add(mustOverflow)

	// Verify that the sum equals the private key of (privA + privB)
	assert.Equal(t, privA, got)
}

func TestPrivate_Sub(t *testing.T) {
	privA := MustGeneratePrivateKeyFromRand(rng)
	privB := MustGeneratePrivateKeyFromRand(rng)

	got := privA.Sub(privB)

	// Verify that the difference equals the private key of (privA - privB)
	wantPubKey := privA.Public().Sub(privB.Public())
	gotPubKey := got.Public()
	assert.Equal(t, wantPubKey, gotPubKey)
}

func TestPrivate_Equals(t *testing.T) {
	priv1 := MustGeneratePrivateKeyFromRand(rng)
	priv2 := MustGeneratePrivateKeyFromRand(rng)

	tests := []struct {
		name string
		a    Private
		b    Private
		want bool
	}{
		{
			name: "same keys",
			a:    priv1,
			b:    priv1,
			want: true,
		},
		{
			name: "different keys",
			a:    priv1,
			b:    priv2,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.Equals(tt.b))
			// Ensure it's commutative
			assert.Equal(t, tt.want, tt.b.Equals(tt.a))
		})
	}
}

func TestPrivate_ToHex(t *testing.T) {
	privKey := MustGeneratePrivateKeyFromRand(rng)

	hexStr := privKey.ToHex()

	// Verify the hex string can be decoded back to the same bytes
	decoded, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	assert.Equal(t, privKey.Serialize(), decoded)
}

func TestPrivate_Value(t *testing.T) {
	privKey := MustGeneratePrivateKeyFromRand(rng)

	value, err := privKey.Value()
	require.NoError(t, err)

	asBytes, ok := value.([]byte)
	assert.True(t, ok)
	assert.Equal(t, privKey.Serialize(), asBytes)
}

func TestPrivate_Scan(t *testing.T) {
	privKey := MustGeneratePrivateKeyFromRand(rng)

	tests := []struct {
		name  string
		input any
		want  secp256k1.PrivateKey
	}{
		{
			name:  "valid key",
			input: &sql.Null[[]byte]{V: privKey.Serialize(), Valid: true},
			want:  privKey.key,
		},
		{
			name:  "valid byte array",
			input: privKey.Serialize(),
			want:  privKey.key,
		},
		{
			name:  "nil value",
			input: nil,
			want:  secp256k1.PrivateKey{},
		},
		{
			name:  "nil sql.Null",
			input: (*sql.Null[[]byte])(nil),
			want:  secp256k1.PrivateKey{},
		},
		{
			name:  "null value",
			input: &sql.Null[[]byte]{Valid: false},
			want:  secp256k1.PrivateKey{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &Private{}
			err := dest.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.want, dest.key)
		})
	}
}

func TestPrivate_Scan_InvalidInput_Errors(t *testing.T) {
	private := &Private{}
	err := private.Scan("not bytes")
	assert.ErrorContains(t, err, "unexpected input for Scan")
}

func TestPrivate_Serialize_Empty_ReturnsEmpty(t *testing.T) {
	pubKeyBytes := Private{}.Serialize()

	assert.Empty(t, pubKeyBytes)
}

func TestPrivate_MarshalJSON(t *testing.T) {
	privKey := MustGeneratePrivateKeyFromRand(rng)
	tests := []struct {
		name string
		key  Private
		want []byte
	}{
		{
			name: "valid key",
			key:  privKey,
			want: privKey.Serialize(),
		},
		{
			name: "empty key",
			key:  Private{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.key.MarshalJSON()
			require.NoError(t, err)

			// Check that the data can be unmarshaled back to the same bytes
			var unmarshaled []byte
			require.NoError(t, json.Unmarshal(data, &unmarshaled))
			assert.Equal(t, tt.want, unmarshaled)
		})
	}
}

func TestPrivate_UnmarshalJSON(t *testing.T) {
	privKey := MustGeneratePrivateKeyFromRand(rng)
	validPrivKeyJson, err := json.Marshal(privKey)
	require.NoError(t, err)

	var dest Private
	require.NoError(t, json.Unmarshal(validPrivKeyJson, &dest))
	assert.Equal(t, privKey, dest)
}

func TestPrivate_UnmarshalJSON_InvalidInput_Errors(t *testing.T) {
	var dest *Private
	err := json.Unmarshal([]byte(`"invalid hex"`), &dest)
	require.Error(t, err)
	assert.Zero(t, dest.key)
}
