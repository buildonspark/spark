package wallet

import (
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
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
	// SparkServiceProviderIdentityPublicKey is the identity public key of the Spark service provider.
	SparkServiceProviderIdentityPublicKey []byte
	// UseTokenTransactionSchnorrSignatures determines whether to use Schnorr signatures (true) or ECDSA signatures (false)
	UseTokenTransactionSchnorrSignatures bool
}

// CoodinatorAddress returns coodinator address.
func (c *Config) CoodinatorAddress() string {
	return c.SigningOperators[c.CoodinatorIdentifier].Address
}

// IdentityPublicKey returns the identity public key.
func (c *Config) IdentityPublicKey() []byte {
	return c.IdentityPrivateKey.PubKey().SerializeCompressed()
}

func (c *Config) ProtoNetwork() pb.Network {
	network, err := common.ProtoNetworkFromNetwork(c.Network)
	if err != nil {
		panic(err)
	}
	return network
}
