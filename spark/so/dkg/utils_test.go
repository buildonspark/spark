package dkg

import (
	"crypto/sha256"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

func TestSignAndVerifyMessage(t *testing.T) {
	msg := []byte("hello world")
	messageHash := sha256.Sum256(msg)
	priv, _ := secp256k1.GeneratePrivateKey()
	signatureBytes, _ := signHash(priv.Serialize(), messageHash[:])

	sig, _ := ecdsa.ParseDERSignature(signatureBytes)
	if !sig.Verify(messageHash[:], priv.PubKey()) {
		panic("signature verification failed")
	}
}
