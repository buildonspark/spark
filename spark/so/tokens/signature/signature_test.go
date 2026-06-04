package signature

import (
	"testing"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/require"
)

func TestGetEffectiveSingleSignatureNilSafe(t *testing.T) {
	require.Nil(t, GetEffectiveSingleSignature(nil))

	sig := []byte{1, 2, 3}
	require.Equal(t, sig, GetEffectiveSingleSignature(&tokenpb.SignatureWithIndex{Signature: sig}))
}
