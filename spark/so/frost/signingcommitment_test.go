package frost

import (
	"database/sql"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbcommon "github.com/lightsparkdev/spark/proto/common"
)

func TestNewSigningCommitment(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	hiding := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	commitment, err := NewSigningCommitment(binding, hiding)

	require.NoError(t, err)
	require.NotNil(t, commitment)
	assert.Equal(t, binding, commitment.binding)
	assert.Equal(t, hiding, commitment.hiding)
}

func TestNewSigningCommitment_ZeroBinding_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.Public{}
	hiding := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	commitment, err := NewSigningCommitment(binding, hiding)

	require.ErrorContains(t, err, "binding must not be zero")
	require.Zero(t, commitment)
}

func TestNewSigningCommitment_ZeroHiding_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	hiding := keys.Public{}

	commitment, err := NewSigningCommitment(binding, hiding)

	require.ErrorContains(t, err, "hiding must not be zero")
	require.Zero(t, commitment)
}

func TestSigningCommitment_Value(t *testing.T) {
	nonce := GenerateSigningNonce()
	commitment := nonce.SigningCommitment()

	value, err := commitment.Value()
	require.NoError(t, err)

	assert.Equal(t, commitment.MarshalBinary(), value)
}

func TestSigningCommitment_Scan(t *testing.T) {
	nonce := GenerateSigningNonce()
	commitment := nonce.SigningCommitment()
	data := commitment.MarshalBinary()

	tests := []struct {
		name               string
		input              any
		expectedCommitment SigningCommitment
	}{
		{
			name:               "valid commitment",
			input:              &sql.Null[[]byte]{V: data, Valid: true},
			expectedCommitment: commitment,
		},
		{
			name:               "nil value",
			input:              nil,
			expectedCommitment: SigningCommitment{},
		},
		{
			name:               "nil sql.Null",
			input:              (*sql.Null[[]byte])(nil),
			expectedCommitment: SigningCommitment{},
		},
		{
			name:               "null value",
			input:              &sql.Null[[]byte]{Valid: false},
			expectedCommitment: SigningCommitment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &SigningCommitment{}
			err := dest.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedCommitment, *dest)
		})
	}
}

func TestSigningCommitment_Scan_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expectedMsg string
	}{
		{name: "not bytes", input: "not bytes", expectedMsg: "unexpected input for Scan: string"},
		{name: "invalid bytes", input: make([]byte, 65), expectedMsg: "failed to scan SigningCommitment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commitment := &SigningCommitment{}
			err := commitment.Scan(tt.input)
			require.ErrorContains(t, err, tt.expectedMsg)
		})
	}
}

func TestSigningCommitment_MarshalBinary(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	hiding := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	commitment, err := NewSigningCommitment(binding, hiding)
	require.NoError(t, err)

	data := commitment.MarshalBinary()

	assert.Len(t, data, 66)
	assert.Equal(t, binding.Serialize(), data[:33])
	assert.Equal(t, hiding.Serialize(), data[33:])
}

func TestSigningCommitment_UnmarshalBinary(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	hiding := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	original, err := NewSigningCommitment(binding, hiding)
	require.NoError(t, err)
	data := original.MarshalBinary()

	dest := SigningCommitment{}
	err = dest.UnmarshalBinary(data)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}

func TestSigningCommitment_UnmarshalBinary_InvalidInput_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	tests := []struct {
		name        string
		input       []byte
		expectedErr string
	}{
		{
			name:        "nil",
			input:       nil,
			expectedErr: "invalid nonce commitment length 0",
		},
		{
			name:        "empty",
			input:       []byte{},
			expectedErr: "invalid nonce commitment length 0",
		},
		{
			name:        "too short",
			input:       make([]byte, 65),
			expectedErr: "invalid nonce commitment length 65",
		},
		{
			name:        "too long",
			input:       make([]byte, 67),
			expectedErr: "invalid nonce commitment length 67",
		},
		{
			name:        "invalid binding",
			input:       append(make([]byte, 33), keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()...),
			expectedErr: "invalid signing commitment binding",
		},
		{
			name:        "invalid hiding",
			input:       append(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(), make([]byte, 33)...),
			expectedErr: "invalid signing commitment hiding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commitment := &SigningCommitment{}
			err := commitment.UnmarshalBinary(tt.input)
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}

func TestSigningCommitment_MarshalProto(t *testing.T) {
	nonce := GenerateSigningNonce()
	commitment := nonce.SigningCommitment()

	proto := commitment.MarshalProto()

	require.NotNil(t, proto)
	assert.Equal(t, commitment.binding.Serialize(), proto.GetBinding())
	assert.Equal(t, commitment.hiding.Serialize(), proto.GetHiding())
}

func TestSigningCommitment_UnmarshalProto(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	binding := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	hiding := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	original, err := NewSigningCommitment(binding, hiding)
	require.NoError(t, err)

	proto := &pbcommon.SigningCommitment{
		Binding: binding.Serialize(),
		Hiding:  hiding.Serialize(),
	}

	dest := SigningCommitment{}
	err = dest.UnmarshalProto(proto)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}

func TestSigningCommitment_UnmarshalProto_InvalidInput_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	tests := []struct {
		name        string
		input       *pbcommon.SigningCommitment
		expectedErr string
	}{
		{
			name:        "nil proto",
			input:       nil,
			expectedErr: "nil proto",
		},
		{
			name: "invalid binding",
			input: &pbcommon.SigningCommitment{
				Binding: make([]byte, 33), // all zeros
				Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			},
			expectedErr: "invalid signing commitment binding",
		},
		{
			name: "invalid hiding",
			input: &pbcommon.SigningCommitment{
				Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
				Hiding:  make([]byte, 33), // all zeros
			},
			expectedErr: "invalid signing commitment hiding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commitment := &SigningCommitment{}
			err := commitment.UnmarshalProto(tt.input)
			assert.ErrorContains(t, err, tt.expectedErr)
		})
	}
}

func TestSigningCommitment_RoundTrip_Binary(t *testing.T) {
	nonce := GenerateSigningNonce()
	original := nonce.SigningCommitment()

	data := original.MarshalBinary()
	dest := SigningCommitment{}
	err := dest.UnmarshalBinary(data)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}

func TestSigningCommitment_RoundTrip_Proto(t *testing.T) {
	nonce := GenerateSigningNonce()
	original := nonce.SigningCommitment()

	proto := original.MarshalProto()

	dest := SigningCommitment{}
	err := dest.UnmarshalProto(proto)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}
