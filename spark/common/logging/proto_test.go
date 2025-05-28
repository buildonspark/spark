package logging

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

func TestProtoWithNilMessage(t *testing.T) {
	formatted, err := FormatProtoMessage(nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", formatted)
}

func TestProtoWithBytes(t *testing.T) {
	signingPk := randomBytes(64)
	rawTx := randomBytes(32)

	signingJob := &pb.SigningJob{
		SigningPublicKey: signingPk,
		RawTx:            rawTx,
	}

	formatted, err := FormatProtoMessage(signingJob)
	require.NoError(t, err)

	expected := fmt.Sprintf(
		`{"signing_public_key":"0x%x","raw_tx":"0x%x"}`,
		signingPk,
		rawTx,
	)

	assert.JSONEq(t, expected, formatted)
}

func TestProtoWithUnspecifiedFields(t *testing.T) {
	// Leave everything except Network unspecified.
	tokenTransaction := &pb.TokenTransaction{
		Network: pb.Network_TESTNET,
	}

	formatted, err := FormatProtoMessage(tokenTransaction)
	require.NoError(t, err)

	expected := `{"token_outputs": [], "spark_operator_identity_public_keys": [], "network": "TESTNET"}`

	assert.JSONEq(t, expected, formatted)
}

func TestProtoWithRedactedFields(t *testing.T) {
	proof1, proof2 := randomBytes(32), randomBytes(32)

	secretShare := &pb.SecretShare{
		SecretShare: randomBytes(32),
		Proofs: [][]byte{
			proof1,
			proof2,
		},
	}

	formatted, err := FormatProtoMessage(secretShare)
	require.NoError(t, err)

	expected := fmt.Sprintf(
		`{"secret_share":"<REDACTED>","proofs":["0x%x","0x%x"]}`,
		proof1,
		proof2,
	)

	assert.JSONEq(t, expected, formatted)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	return b
}
