package common

import (
	"crypto/sha256"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/assert"
)

func TestAdaptorSignature(t *testing.T) {
	failures := 0
	for i := 0; i < 1000; i++ {
		privKey, err := btcec.NewPrivateKey()
		assert.NoError(t, err)
		pubkey := privKey.PubKey()

		msg := []byte("test")
		hash := sha256.Sum256(msg)
		sig, err := schnorr.Sign(privKey, hash[:], schnorr.FastSign())
		assert.NoError(t, err)

		assert.True(t, sig.Verify(hash[:], pubkey))

		adaptorSig, adaptorPrivKey, err := GenerateAdaptorFromSignature(sig.Serialize())
		assert.NoError(t, err)

		_, adaptorPub := btcec.PrivKeyFromBytes(adaptorPrivKey)

		err = ValidateAdaptorSignature(pubkey, hash[:], adaptorSig, adaptorPub.SerializeCompressed())
		assert.NoError(t, err)
	}

	assert.Zero(t, failures)
}
