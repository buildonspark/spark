package wallet

import (
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
)

// Config is the configuration for the wallet.
type Config struct {
	// Network is the network to use for the wallet.
	Network common.Network
	// SigningOperators contains all the signing operators using identifier as key.
	SigningOperators map[string]*so.SigningOperator
	// CoodinatorIdentifier is the identifier of the signing operator as the coodinator.
	CoodinatorIdentifier string
	// FrostSignerAddress is the address of the Frost signer.
	FrostSignerAddress string
	// IdentityPrivateKey is the identity private key of the wallet.
	IdentityPrivateKey secp256k1.PrivateKey
	// Threshold is the min signing operators.
	Threshold int
}

// CoodinatorAddress returns coodinator address.
func (c *Config) CoodinatorAddress() string {
	return c.SigningOperators[c.CoodinatorIdentifier].Address
}

// IdentityPublicKey returns the identity public key.
func (c *Config) IdentityPublicKey() []byte {
	return c.IdentityPrivateKey.PubKey().SerializeCompressed()
}
