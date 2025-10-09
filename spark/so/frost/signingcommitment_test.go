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
		name  string
		input any
		want  SigningCommitment
	}{
		{
			name:  "valid commitment",
			input: &sql.Null[[]byte]{V: data, Valid: true},
			want:  commitment,
		},
		{
			name:  "nil value",
			input: nil,
			want:  SigningCommitment{},
		},
		{
			name:  "nil sql.Null",
			input: (*sql.Null[[]byte])(nil),
			want:  SigningCommitment{},
		},
		{
			name:  "null value",
			input: &sql.Null[[]byte]{Valid: false},
			want:  SigningCommitment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := &SigningCommitment{}
			err := dest.Scan(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.want, *dest)
		})
	}
}

func TestSigningCommitment_Scan_InvalidInput_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		wantMsg string
	}{
		{name: "not bytes", input: "not bytes", wantMsg: "unexpected input for Scan: string"},
		{name: "invalid bytes", input: make([]byte, 65), wantMsg: "failed to scan SigningCommitment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commitment := &SigningCommitment{}
			err := commitment.Scan(tt.input)
			require.ErrorContains(t, err, tt.wantMsg)
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
		name    string
		input   []byte
		wantErr string
	}{
		{
			name:    "nil",
			input:   nil,
			wantErr: "invalid nonce commitment length 0",
		},
		{
			name:    "empty",
			input:   []byte{},
			wantErr: "invalid nonce commitment length 0",
		},
		{
			name:    "too short",
			input:   make([]byte, 65),
			wantErr: "invalid nonce commitment length 65",
		},
		{
			name:    "too long",
			input:   make([]byte, 67),
			wantErr: "invalid nonce commitment length 67",
		},
		{
			name:    "invalid binding",
			input:   append(make([]byte, 33), keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()...),
			wantErr: "invalid signing commitment binding",
		},
		{
			name:    "invalid hiding",
			input:   append(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(), make([]byte, 33)...),
			wantErr: "invalid signing commitment hiding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commitment := &SigningCommitment{}
			err := commitment.UnmarshalBinary(tt.input)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSigningCommitment_MarshalProto(t *testing.T) {
	nonce := GenerateSigningNonce()
	commitment := nonce.SigningCommitment()

	proto, err := commitment.MarshalProto()

	require.NoError(t, err)
	require.NotNil(t, proto)
	assert.Equal(t, commitment.binding.Serialize(), proto.Binding)
	assert.Equal(t, commitment.hiding.Serialize(), proto.Hiding)
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
		name    string
		input   *pbcommon.SigningCommitment
		wantErr string
	}{
		{
			name:    "nil proto",
			input:   nil,
			wantErr: "nil proto",
		},
		{
			name: "invalid binding",
			input: &pbcommon.SigningCommitment{
				Binding: make([]byte, 33), // all zeros
				Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			},
			wantErr: "invalid signing commitment binding",
		},
		{
			name: "invalid hiding",
			input: &pbcommon.SigningCommitment{
				Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
				Hiding:  make([]byte, 33), // all zeros
			},
			wantErr: "invalid signing commitment hiding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commitment := &SigningCommitment{}
			err := commitment.UnmarshalProto(tt.input)
			assert.ErrorContains(t, err, tt.wantErr)
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

	proto, _ := original.MarshalProto()

	dest := SigningCommitment{}
	err := dest.UnmarshalProto(proto)

	require.NoError(t, err)
	assert.Equal(t, original, dest)
}
