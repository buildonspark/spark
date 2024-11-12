package dkg

import (
	"crypto/sha256"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1"
)

func TestSignAndVerifyMessage(t *testing.T) {
	msg := []byte("hello world")
	messageHash := sha256.Sum256(msg)
	priv, _ := secp256k1.GeneratePrivateKey()
	signatureBytes, _ := signHash(priv.Serialize(), messageHash[:])

	sig, _ := secp256k1.ParseDERSignature(signatureBytes)
	if !sig.Verify(messageHash[:], priv.PubKey()) {
		panic("signature verification failed")
	}
}
