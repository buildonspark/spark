package wallet

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

// NewTestWalletConfig returns a wallet configuration that can be used for testing.
func NewTestWalletConfig(tb testing.TB) *TestWalletConfig {
	identityPrivKey := keys.GeneratePrivateKey()
	return NewTestWalletConfigWithIdentityKey(tb, identityPrivKey)
}

// NewTestWalletConfigWithIdentityKey returns a wallet configuration with specified identity key that can be used for testing.
func NewTestWalletConfigWithIdentityKey(tb testing.TB, identityPrivKey keys.Private) *TestWalletConfig {
	return NewTestWalletConfigWithParams(tb,
		TestWalletConfigParams{
			IdentityPrivateKey: identityPrivKey,
		})
}

// NewTestWalletConfigWithIdentityKeyAndCoordinator returns a wallet configuration with specified identity key that can be used for testing.
func NewTestWalletConfigWithIdentityKeyAndCoordinator(tb testing.TB, identityPrivKey keys.Private, coordinatorIndex int) *TestWalletConfig {
	return NewTestWalletConfigWithParams(tb,
		TestWalletConfigParams{
			IdentityPrivateKey: identityPrivKey,
			CoordinatorIndex:   coordinatorIndex,
		})
}

// TestWalletConfigParams defines optional parameters for generating a test wallet configuration.
type TestWalletConfigParams struct {
	// CoordinatorIndex selects which operator should be considered the coordinator for this wallet
	// configuration. Defaults to index 0.
	CoordinatorIndex int

	// IdentityPrivateKey allows callers to supply a deterministic identity key. If empty, a new
	// key will be generated.
	IdentityPrivateKey keys.Private

	// UseTokenTransactionSchnorrSignatures toggles Schnorr vs ECDSA signatures when constructing
	// transactions in tests.
	UseTokenTransactionSchnorrSignatures bool

	// Network allows callers to override the default network (Regtest).
	Network common.Network
}

// NewTestWalletConfigWithParams creates a wallet.Config suitable for tests using the provided parameters.
func NewTestWalletConfigWithParams(tb testing.TB, p TestWalletConfigParams) *TestWalletConfig {
	rng := rand.NewChaCha8([32]byte{1})

	if p.CoordinatorIndex < 0 {
		p.CoordinatorIndex = 0
	}

	var privKey keys.Private
	if p.IdentityPrivateKey.IsZero() {
		privKey = keys.GeneratePrivateKey()
	} else {
		privKey = p.IdentityPrivateKey
	}

	signingOperators := sparktesting.GetAllSigningOperators(tb)

	network := common.Regtest
	if p.Network != common.Unspecified {
		network = p.Network
	}

	coordinatorIdentifier := fmt.Sprintf("%064d", p.CoordinatorIndex+1)
	return &TestWalletConfig{
		Network:                               network,
		SigningOperators:                      signingOperators,
		CoordinatorIdentifier:                 coordinatorIdentifier,
		FrostSignerAddress:                    sparktesting.GetLocalFrostSignerAddress(tb),
		IdentityPrivateKey:                    privKey,
		Threshold:                             3,
		SparkServiceProviderIdentityPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public(),
		UseTokenTransactionSchnorrSignatures:  p.UseTokenTransactionSchnorrSignatures,
		CoordinatorDatabaseURI:                sparktesting.GetTestDatabasePath(p.CoordinatorIndex),
		FrostGRPCConnectionFactory:            &sparktesting.TestGRPCConnectionFactory{},
	}
}
