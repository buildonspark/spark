package common

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/assert"
)

func TestAdaptorSignature(t *testing.T) {
	for range 1000 {
		privKey, err := btcec.NewPrivateKey()
		require.NoError(t, err)
		pubkey := privKey.PubKey()

		msg := []byte("test")
		hash := sha256.Sum256(msg)
		sig, err := schnorr.Sign(privKey, hash[:], schnorr.FastSign())
		require.NoError(t, err)

		assert.True(t, sig.Verify(hash[:], pubkey))

		adaptorSig, adaptorPrivKey, err := GenerateAdaptorFromSignature(sig.Serialize())
		require.NoError(t, err)

		_, adaptorPub := btcec.PrivKeyFromBytes(adaptorPrivKey)

		err = ValidateOutboundAdaptorSignature(pubkey, hash[:], adaptorSig, adaptorPub.SerializeCompressed())
		require.NoError(t, err)

		adaptorSig, err = ApplyAdaptorToSignature(pubkey, hash[:], adaptorSig, adaptorPrivKey)
		require.NoError(t, err)

		newSig, err := schnorr.ParseSignature(adaptorSig)
		require.NoError(t, err)

		assert.True(t, newSig.Verify(hash[:], pubkey))
	}
}
