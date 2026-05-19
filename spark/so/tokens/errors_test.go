package tokens

import (
	stderrors "errors"
	"testing"

	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/require"
)

func TestFormatErrorWithTransactionProtoHandlesNilCreatedOutput(t *testing.T) {
	tx := &tokenpb.TokenTransaction{
		Version: 1,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{IssuerPublicKey: []byte{0x02}},
		},
		TokenOutputs: []*tokenpb.TokenOutput{nil},
		Network:      pb.Network_REGTEST,
	}

	var err error
	require.NotPanics(t, func() {
		err = FormatErrorWithTransactionProto("invalid token transaction", tx, stderrors.New("boom"))
	})
	require.ErrorContains(t, err, "invalid token transaction")
	require.ErrorContains(t, err, "created_outputs")
	require.ErrorContains(t, err, "<nil>")
}
