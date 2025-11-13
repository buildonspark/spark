package wallet

import (
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/frost"
	"google.golang.org/grpc"
)

// TestWalletConfig is the configuration for the wallet.
type TestWalletConfig struct {
	// Network is the network to use for the wallet.
	Network common.Network
	// SigningOperators contains all the signing operators using identifier as key.
	SigningOperators map[string]*so.SigningOperator
	// CoordinatorIdentifier is the identifier of the signing operator as the coordinator.
	CoordinatorIdentifier string
	// FrostSignerAddress is the address of the Frost signer.
	FrostSignerAddress string
	// IdentityPrivateKey is the identity private key of the wallet.
	IdentityPrivateKey keys.Private
	// Threshold is the min signing operators.
	Threshold int
	// SparkServiceProviderIdentityPublicKey is the identity public key of the Spark service provider.
	SparkServiceProviderIdentityPublicKey keys.Public
	// UseTokenTransactionSchnorrSignatures determines whether to use Schnorr signatures (true) or ECDSA signatures (false)
	UseTokenTransactionSchnorrSignatures bool
	// CoordinatorDatabaseURI is the database URI for the coordinator.
	CoordinatorDatabaseURI string
	// FrostGRPCConnectionFactory is the factory for creating gRPC connections to the Frost signer. Used for subbing in test connections.
	FrostGRPCConnectionFactory frost.FrostGRPCConnectionFactory
}

// CoordinatorAddress returns coordinator address.
func (c *TestWalletConfig) CoordinatorAddress() string {
	return c.SigningOperators[c.CoordinatorIdentifier].AddressRpc
}

// IdentityPublicKey returns the identity public key.
func (c *TestWalletConfig) IdentityPublicKey() keys.Public {
	return c.IdentityPrivateKey.Public()
}

func (c *TestWalletConfig) ProtoNetwork() pb.Network {
	network, err := common.ProtoNetworkFromNetwork(c.Network)
	if err != nil {
		panic(err)
	}
	return network
}

func (c *TestWalletConfig) NewCoordinatorGRPCConnection() (so.OperatorClientConn, error) {
	return c.SigningOperators[c.CoordinatorIdentifier].NewOperatorGRPCConnection()
}

func (c *TestWalletConfig) NewFrostGRPCConnection() (*grpc.ClientConn, error) {
	return c.FrostGRPCConnectionFactory.NewFrostGRPCConnection(c.FrostSignerAddress)
}
