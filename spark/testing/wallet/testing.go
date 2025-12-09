package wallet

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

const (
	defaultWithdrawBondSats              = 10000
	defaultWithdrawRelativeBlockLocktime = 1000
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
			Network:            btcnetwork.Regtest,
			IdentityPrivateKey: identityPrivKey,
		})
}

// NewTestWalletConfigWithIdentityKeyAndCoordinator returns a wallet configuration with specified identity key that can be used for testing.
func NewTestWalletConfigWithIdentityKeyAndCoordinator(tb testing.TB, identityPrivKey keys.Private, coordinatorIndex int) *TestWalletConfig {
	return NewTestWalletConfigWithParams(tb,
		TestWalletConfigParams{
			Network:            btcnetwork.Regtest,
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
	Network btcnetwork.Network

	// WithdrawBondSats overrides the expected withdraw bond amount (defaults to the local test config value).
	WithdrawBondSats uint64
	// WithdrawRelativeBlockLocktime overrides the expected withdraw locktime (defaults to the local test config value).
	WithdrawRelativeBlockLocktime uint64
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
	threshold := int(math.Floor(float64(len(signingOperators)+2) / 2))

	network := btcnetwork.Regtest
	if p.Network != btcnetwork.Unspecified {
		network = p.Network
	}

	withdrawBondSats := p.WithdrawBondSats
	if withdrawBondSats == 0 {
		withdrawBondSats = defaultWithdrawBondSats
	}

	withdrawRelativeBlockLocktime := p.WithdrawRelativeBlockLocktime
	if withdrawRelativeBlockLocktime == 0 {
		withdrawRelativeBlockLocktime = defaultWithdrawRelativeBlockLocktime
	}

	coordinatorIdentifier := fmt.Sprintf("%064d", p.CoordinatorIndex+1)
	return &TestWalletConfig{
		Network:                               network,
		SigningOperators:                      signingOperators,
		CoordinatorIdentifier:                 coordinatorIdentifier,
		FrostSignerAddress:                    sparktesting.GetLocalFrostSignerAddress(tb),
		IdentityPrivateKey:                    privKey,
		Threshold:                             threshold,
		SparkServiceProviderIdentityPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public(),
		UseTokenTransactionSchnorrSignatures:  p.UseTokenTransactionSchnorrSignatures,
		CoordinatorDatabaseURI:                sparktesting.GetTestDatabasePath(p.CoordinatorIndex),
		FrostGRPCConnectionFactory:            &sparktesting.TestGRPCConnectionFactory{},
		WithdrawBondSats:                      withdrawBondSats,
		WithdrawRelativeBlockLocktime:         withdrawRelativeBlockLocktime,
	}
}
