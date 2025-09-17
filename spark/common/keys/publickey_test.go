package keys

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

var rng = rand.NewChaCha8([32]byte{1})

func TestParsePubKey(t *testing.T) {
	pubKeyBytes := MustGeneratePrivateKeyFromRand(rng).Public().Serialize()

	result, err := ParsePublicKey(pubKeyBytes)

	require.NoError(t, err)
	assert.Equal(t, pubKeyBytes, result.Serialize())
}

func TestParsePubKey_InvalidInput_Errors(t *testing.T) {
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
			input:   bytes.Repeat([]byte{1}, 32),
			wantErr: "malformed public key: invalid length: 32",
		},
		{
			name:    "too long",
			input:   bytes.Repeat([]byte{1}, 34),
			wantErr: "malformed public key: invalid length: 34",
		},
		{
			name:    "invalid format",
			input:   bytes.Repeat([]byte{0}, 33),
			wantErr: "invalid public key: unsupported format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePublicKey(tt.input)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestParsePubKeyMap(t *testing.T) {
	pub1 := MustGeneratePrivateKeyFromRand(rng).Public()
	pub2 := MustGeneratePrivateKeyFromRand(rng).Public()
	asBytes := map[string][]byte{
		"key1": pub1.Serialize(),
		"key2": pub2.Serialize(),
	}

	got, err := ParsePublicKeyMap(asBytes)
	require.NoError(t, err)

	want := map[string]Public{"key1": pub1, "key2": pub2}
	assert.Equal(t, want, got)
}

func TestParsePubKeyMap_InvalidKey_Errors(t *testing.T) {
	pub1 := MustGeneratePrivateKeyFromRand(rng).Public()
	asBytes := map[string][]byte{
		"key1": pub1.Serialize(),
		"key2": bytes.Repeat([]byte{0}, 33), // invalid key
	}

	_, err := ParsePublicKeyMap(asBytes)

	assert.ErrorContains(t, err, "invalid public key: unsupported format")
}

func TestPublicKeyFromInts_NormalizesInputs(t *testing.T) {
	// secp256k1 prime + 1 (aka 0), not normalized
	decoded, err := hex.DecodeString("fffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc2f11")
	require.NoError(t, err)

	notNormal := new(big.Int).SetBytes(decoded)
	zero := big.NewInt(0)

	assert.Equal(t, publicKeyFromInts(zero, zero), publicKeyFromInts(notNormal, notNormal))
}

func TestPublic_Add(t *testing.T) {
	privA := MustGeneratePrivateKeyFromRand(rng)
	privB := MustGeneratePrivateKeyFromRand(rng)

	got := privA.Public().Add(privB.Public())

	// Verify that the sum equals the public key of (privA + privB)
	want := privA.Add(privB).Public()
	assert.Equal(t, want, got)
}

func TestPublic_AddTweak(t *testing.T) {
	privKey := MustGeneratePrivateKeyFromRand(rng)
	tweak := MustGeneratePrivateKeyFromRand(rng)

	got := privKey.Public().AddTweak(tweak)

	// Verify that the result equals the public key of (privKey + tweak)
	want := privKey.Add(tweak).Public()
	assert.Equal(t, want, got)
}

func TestPublic_Sub(t *testing.T) {
	privA := MustGeneratePrivateKeyFromRand(rng)
	privB := MustGeneratePrivateKeyFromRand(rng)

	got := privA.Public().Sub(privB.Public())

	// Verify that the difference equals the public key of (privA - privB)
	want := privA.Sub(privB).Public()
	assert.Equal(t, want, got)
}

func TestPublic_Equals(t *testing.T) {
	priv1 := MustGeneratePrivateKeyFromRand(rng)
	priv2 := MustGeneratePrivateKeyFromRand(rng)

	tests := []struct {
		name string
		a    Public
		b    Public
		want bool
	}{
		{
			name: "same keys",
			a:    priv1.Public(),
			b:    priv1.Public(),
			want: true,
		},
		{
			name: "different keys",
			a:    priv1.Public(),
			b:    priv2.Public(),
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

func TestPublic_ToHex(t *testing.T) {
	pubKey := MustGeneratePrivateKeyFromRand(rng).Public()

	hexStr := pubKey.ToHex()

	// Verify the hex string can be decoded back to the same bytes
	decoded, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	assert.Equal(t, pubKey.Serialize(), decoded)
}

func TestSumPublicKeys(t *testing.T) {
	priv1 := MustGeneratePrivateKeyFromRand(rng)
	priv2 := MustGeneratePrivateKeyFromRand(rng)
	priv3 := MustGeneratePrivateKeyFromRand(rng)
	priv4 := MustGeneratePrivateKeyFromRand(rng)

	public1 := priv1.Public()
	public2 := priv2.Public()
	public3 := priv3.Public()
	public4 := priv4.Public()

	tests := []struct {
		name string
		keys []Public
		want Public
	}{
		{
			name: "single key",
			keys: []Public{public1},
			want: public1,
		},
		{
			name: "two keys",
			keys: []Public{public1, public2},
			want: priv1.Add(priv2).Public(),
		},
		{
			name: "three keys",
			keys: []Public{public1, public2, public3},
			want: priv1.Add(priv2).Add(priv3).Public(),
		},
		{
			name: "four keys",
			keys: []Public{public1, public2, public3, public4},
			want: priv1.Add(priv2).Add(priv3).Add(priv4).Public(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := SumPublicKeys(tt.keys)
			require.NoError(t, err)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestSumPublicKeys_EmptyList_Errors(t *testing.T) {
	_, err := SumPublicKeys([]Public{})
	require.ErrorContains(t, err, "no keys to add")
}

func TestPublic_Value(t *testing.T) {
	pubKey := MustGeneratePrivateKeyFromRand(rng).Public()

	value, err := pubKey.Value()
	require.NoError(t, err)

	assert.Equal(t, pubKey.Serialize(), value)
}

func TestPublic_Scan(t *testing.T) {
	pubKey := MustGeneratePrivateKeyFromRand(rng).Public()

	tests := []struct {
		name  string
		input any
		want  secp256k1.PublicKey
	}{
		{
			name:  "valid key",
			input: &sql.Null[[]byte]{V: pubKey.Serialize(), Valid: true},
			want:  pubKey.key,
		},
		{
			name:  "nil value",
			input: nil,
			want:  secp256k1.PublicKey{},
		},
		{
			name:  "nil sql.Null",
			input: (*sql.Null[[]byte])(nil),
			want:  secp256k1.PublicKey{},
		},
		{
			name:  "null value",
			input: &sql.Null[[]byte]{Valid: false},
			want:  secp256k1.PublicKey{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &Public{}
			err := dest.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.want, dest.key)
		})
	}
}

func TestPublic_Scan_InvalidInput_Errors(t *testing.T) {
	public := &Public{}
	err := public.Scan("not bytes")
	assert.ErrorContains(t, err, "unexpected input for Scan")
}

func TestPublic_Serialize_Empty_ReturnsEmpty(t *testing.T) {
	pubKeyBytes := Public{}.Serialize()

	assert.Empty(t, pubKeyBytes)
}

func TestPublic_MarshalJSON(t *testing.T) {
	pubKey := MustGeneratePrivateKeyFromRand(rng).Public()
	tests := []struct {
		name string
		key  Public
		want []byte
	}{
		{
			name: "valid key",
			key:  pubKey,
			want: pubKey.Serialize(),
		},
		{
			name: "empty key",
			key:  Public{},
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

func TestPublic_UnmarshalJSON(t *testing.T) {
	pubKey := MustGeneratePrivateKeyFromRand(rng).Public()
	validPubKeyJson, err := json.Marshal(pubKey)
	require.NoError(t, err)

	var dest Public
	require.NoError(t, json.Unmarshal(validPubKeyJson, &dest))
	assert.Equal(t, pubKey, dest)
}

func TestPublic_UnmarshalJSON_InvalidInput_Errors(t *testing.T) {
	var dest *Public
	err := json.Unmarshal([]byte(`"invalid hex"`), &dest)
	require.Error(t, err)
	assert.Zero(t, dest.key)
}

func TestToBytesMap(t *testing.T) {
	private := MustGeneratePrivateKeyFromRand(rng)
	public := private.Public()

	tests := []struct {
		name  string
		value map[string]Public
		want  map[string][]byte
	}{
		{
			name:  "nil",
			value: nil,
			want:  nil,
		},
		{
			name:  "empty",
			value: map[string]Public{},
			want:  nil,
		},
		{
			name:  "public",
			value: map[string]Public{"abc": public},
			want:  map[string][]byte{"abc": public.Serialize()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ToBytesMap(tt.value))
		})
	}

	// We have to test this separately because [key] can only be used for generics.
	t.Run("private", func(t *testing.T) {
		got := ToBytesMap(map[string]Private{"abc": private})
		want := map[string][]byte{"abc": private.Serialize()}
		assert.Equal(t, want, got)
	})
}
