package authninternal

import (
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionTokenCreatorVerifier_VerifyToken_InvalidBase64(t *testing.T) {
	identityKey, _ := secp256k1.GeneratePrivateKey()
	verifier, err := NewSessionTokenCreatorVerifier(identityKey.Serialize(), RealClock{})
	require.NoError(t, err)

	session, err := verifier.VerifyToken("not-base64!@#$")

	assert.ErrorIs(t, err, ErrInvalidTokenEncoding)
	assert.Nil(t, session)
}

func TestSessionTokenCreatorVerifier_VerifyToken_ValidBase64InvalidProtobuf(t *testing.T) {
	identityKey, _ := secp256k1.GeneratePrivateKey()
	verifier, err := NewSessionTokenCreatorVerifier(identityKey.Serialize(), RealClock{})
	require.NoError(t, err)

	session, err := verifier.VerifyToken("SGVsbG8gV29ybGQ=") // "Hello World" in base64

	assert.Error(t, err)
	assert.Nil(t, session)
}
