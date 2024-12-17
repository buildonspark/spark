package wallet

import (
	"github.com/decred/dcrd/dcrec/secp256k1"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
)

// CreateUserKeyPackage creates a user frost signing key package from a signing private key.
func CreateUserKeyPackage(signingPrivateKey []byte) *pbfrost.KeyPackage {
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	_, pubkey := secp256k1.PrivKeyFromBytes(signingPrivateKey)
	userKeyPackage := &pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: signingPrivateKey,
		PublicShares: map[string][]byte{
			userIdentifier: pubkey.SerializeCompressed(),
		},
		PublicKey:  pubkey.SerializeCompressed(),
		MinSigners: 1,
	}
	return userKeyPackage
}
