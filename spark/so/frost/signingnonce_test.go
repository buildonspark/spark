package frost

import (
	"database/sql"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbfrost "github.com/lightsparkdev/spark/proto/frost"
)

func TestGenerateSigningNonce(t *testing.T) {
	nonce := GenerateSigningNonce()

	require.NotNil(t, nonce)
	assert.NotZero(t, nonce.binding)
	assert.NotZero(t, nonce.hiding)
	assert.NotEqual(t, nonce.binding, nonce.hiding)
}

func TestNewSigningNonce(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng)
	hiding := keys.MustGeneratePrivateKeyFromRand(rng)

	nonce, err := NewSigningNonce(binding, hiding)

	require.NoError(t, err)
	assert.Equal(t, binding, nonce.binding)
	assert.Equal(t, hiding, nonce.hiding)
}

func TestNewSigningNonce_EmptyKey_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	valid := keys.MustGeneratePrivateKeyFromRand(rng)

	t.Run("binding empty", func(t *testing.T) {
		nonce, err := NewSigningNonce(keys.Private{}, valid)
		require.Error(t, err)
		assert.Zero(t, nonce)
	})

	t.Run("hiding empty", func(t *testing.T) {
		nonce, err := NewSigningNonce(valid, keys.Private{})
		require.Error(t, err)
		assert.Zero(t, nonce)
	})
}

func TestSigningNonce_SigningCommitment(t *testing.T) {
	nonce := GenerateSigningNonce()

	commitment := nonce.SigningCommitment()

	require.NotNil(t, commitment)
	assert.Equal(t, nonce.binding.Public(), commitment.binding)
	assert.Equal(t, nonce.hiding.Public(), commitment.hiding)
}

func TestSigningNonce_Value(t *testing.T) {
	nonce := GenerateSigningNonce()

	value, err := nonce.Value()
	require.NoError(t, err)

	assert.Equal(t, nonce.MarshalBinary(), value)
}

func TestSigningNonce_Scan(t *testing.T) {
	nonce := GenerateSigningNonce()
	data := nonce.MarshalBinary()

	tests := []struct {
		name  string
		input any
		want  SigningNonce
	}{
		{
			name:  "valid nonce",
			input: data,
			want:  nonce,
		},
		{
			name:  "valid wrapped nonce",
			input: &sql.Null[[]byte]{V: data, Valid: true},
			want:  nonce,
		},
		{
			name:  "nil value",
			input: nil,
			want:  SigningNonce{},
		},
		{
			name:  "nil sql.Null",
			input: (*sql.Null[[]byte])(nil),
			want:  SigningNonce{},
		},
		{
			name:  "null value",
			input: &sql.Null[[]byte]{Valid: false},
			want:  SigningNonce{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &SigningNonce{}
			err := dest.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.want, *dest)
		})
	}
}

func TestSigningNonce_Scan_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		wantMsg string
	}{
		{name: "not bytes", input: "not bytes", wantMsg: "unexpected input for Scan: string"},
		{name: "invalid bytes", input: make([]byte, 65), wantMsg: "failed to scan SigningNonce"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nonce := &SigningNonce{}
			err := nonce.Scan(tt.input)
			require.ErrorContains(t, err, tt.wantMsg)
		})
	}
}

func TestSigningNonce_MarshalBinary(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng)
	hiding := keys.MustGeneratePrivateKeyFromRand(rng)
	nonce, err := NewSigningNonce(binding, hiding)
	require.NoError(t, err)

	data := nonce.MarshalBinary()

	assert.Len(t, data, 64)
	assert.Equal(t, binding.Serialize(), data[:32])
	assert.Equal(t, hiding.Serialize(), data[32:])
}

func TestSigningNonce_UnmarshalBinary(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng)
	hiding := keys.MustGeneratePrivateKeyFromRand(rng)
	original, err := NewSigningNonce(binding, hiding)
	require.NoError(t, err)
	data := original.MarshalBinary()

	dest := SigningNonce{}
	err = dest.UnmarshalBinary(data)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}

func TestSigningNonce_UnmarshalBinary_InvalidInput_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	tests := []struct {
		name    string
		input   []byte
		wantErr string
	}{
		{
			name:    "nil",
			input:   nil,
			wantErr: "invalid nonce length 0",
		},
		{
			name:    "empty",
			input:   []byte{},
			wantErr: "invalid nonce length 0",
		},
		{
			name:    "too short",
			input:   make([]byte, 63),
			wantErr: "invalid nonce length 63",
		},
		{
			name:    "too long",
			input:   make([]byte, 65),
			wantErr: "invalid nonce length 65",
		},
		{
			name:    "invalid binding",
			input:   append(make([]byte, 32), keys.MustGeneratePrivateKeyFromRand(rng).Serialize()...),
			wantErr: "invalid signing nonce binding",
		},
		{
			name:    "invalid hiding",
			input:   append(keys.MustGeneratePrivateKeyFromRand(rng).Serialize(), make([]byte, 32)...),
			wantErr: "invalid signing nonce hiding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nonce := &SigningNonce{}
			err := nonce.UnmarshalBinary(tt.input)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSigningNonce_MarshalProto(t *testing.T) {
	nonce := GenerateSigningNonce()

	proto, err := nonce.MarshalProto()

	require.NoError(t, err)
	require.NotNil(t, proto)
	assert.Equal(t, nonce.binding.Serialize(), proto.Binding)
	assert.Equal(t, nonce.hiding.Serialize(), proto.Hiding)
}

func TestSigningNonce_UnmarshalProto(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng)
	hiding := keys.MustGeneratePrivateKeyFromRand(rng)
	original, err := NewSigningNonce(binding, hiding)
	require.NoError(t, err)

	proto := &pbfrost.SigningNonce{
		Binding: binding.Serialize(),
		Hiding:  hiding.Serialize(),
	}

	dest := SigningNonce{}
	err = dest.UnmarshalProto(proto)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}

func TestSigningNonce_UnmarshalProto_InvalidInput_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	tests := []struct {
		name    string
		input   *pbfrost.SigningNonce
		wantErr string
	}{
		{
			name:    "nil proto",
			input:   nil,
			wantErr: "nil proto",
		},
		{
			name: "invalid binding",
			input: &pbfrost.SigningNonce{
				Binding: make([]byte, 32), // all zeros
				Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Serialize(),
			},
			wantErr: "invalid signing nonce binding",
		},
		{
			name: "invalid hiding",
			input: &pbfrost.SigningNonce{
				Binding: keys.MustGeneratePrivateKeyFromRand(rng).Serialize(),
				Hiding:  make([]byte, 32), // all zeros
			},
			wantErr: "invalid signing nonce hiding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nonce := &SigningNonce{}
			err := nonce.UnmarshalProto(tt.input)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSigningNonce_RoundTrip_Binary(t *testing.T) {
	original := GenerateSigningNonce()

	data := original.MarshalBinary()
	dest := SigningNonce{}
	err := dest.UnmarshalBinary(data)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}

func TestSigningNonce_RoundTrip_Proto(t *testing.T) {
	original := GenerateSigningNonce()

	proto, _ := original.MarshalProto()

	dest := SigningNonce{}
	err := dest.UnmarshalProto(proto)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}
