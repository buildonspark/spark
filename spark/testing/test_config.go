package sparktesting

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lightsparkdev/spark/common"
	sparkgrpc "github.com/lightsparkdev/spark/common/grpc"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/testing/wallet"
)

var rng = rand.NewChaCha8([32]byte{1})

const (
	minikubeCAFilePath    = "/tmp/minikube-ca.pem"
	signingOperatorPrefix = "000000000000000000000000000000000000000000000000000000000000000"
)

func isHermeticTest() bool {
	return os.Getenv("HERMETIC_TEST") == "true"
}

// IsGripmock returns true if the GRIPMOCK environment variable is set to true.
func IsGripmock() bool {
	return os.Getenv("GRIPMOCK") == "true"
}

// Common pubkeys used for both hermetic and local test environments
var testOperatorPubkeys = []keys.Public{
	keys.MustParsePublicKeyHex("0322ca18fc489ae25418a0e768273c2c61cabb823edfb14feb891e9bec62016510"),
	keys.MustParsePublicKeyHex("0341727a6c41b168f07eb50865ab8c397a53c7eef628ac1020956b705e43b6cb27"),
	keys.MustParsePublicKeyHex("0305ab8d485cc752394de4981f8a5ae004f2becfea6f432c9a59d5022d8764f0a6"),
	keys.MustParsePublicKeyHex("0352aef4d49439dedd798ac4aef1e7ebef95f569545b647a25338398c1247ffdea"),
	keys.MustParsePublicKeyHex("02c05c88cc8fc181b1ba30006df6a4b0597de6490e24514fbdd0266d2b9cd3d0ba"),
}

var testOperatorPrivkeys = []keys.Private{
	keys.MustParsePrivateKeyHex("5eaae81bcf1fd43fbb92432b82dbafc8273bb3287b42cb4cf3c851fcee2212a5"),
	keys.MustParsePrivateKeyHex("bc0f5b9055c4a88b881d4bb48d95b409cd910fb27c088380f8ecda2150ee8faf"),
	keys.MustParsePrivateKeyHex("d5043294f686bc1e3337ce4a44801b011adc67524175f27d7adc85d81d6a4545"),
	keys.MustParsePrivateKeyHex("f2136e83e8dc4090291faaaf5ea21a27581906d8b108ac0eefdaecf4ee86ac99"),
	keys.MustParsePrivateKeyHex("effe79dc2a911a5a359910cb7782f5cabb3b7cf01e3809f8d323898ffd78e408"),
}

type TestGRPCConnectionFactory struct {
	timeoutProvider *common.ClientTimeoutConfig
}

func (f *TestGRPCConnectionFactory) NewFrostGRPCConnection(frostSignerAddress string) (*grpc.ClientConn, error) {
	return DangerousNewGRPCConnectionWithoutTLS(frostSignerAddress, nil)
}

func (f *TestGRPCConnectionFactory) SetTimeoutProvider(timeoutProvider sparkgrpc.TimeoutProvider) {
	f.timeoutProvider = &common.ClientTimeoutConfig{
		TimeoutProvider: timeoutProvider,
	}
}

func operatorCount(tb testing.TB) int {
	if envOpCount := os.Getenv("NUM_SPARK_OPERATORS"); envOpCount != "" {
		if n, err := strconv.Atoi(envOpCount); err == nil {
			if n > 0 && n <= len(testOperatorPubkeys) {
				return n
			} else {
				tb.Fatalf("Invalid NUM_SPARK_OPERATORS value: %s. Must be between 1 and %d", envOpCount, len(testOperatorPubkeys))
			}
		} else {
			tb.Fatalf("Error converting NUM_SPARK_OPERATORS to integer: %v", err)
		}
	}
	// default to all test operators
	return len(testOperatorPubkeys)
}

func GetAllSigningOperators(tb testing.TB) map[string]*so.SigningOperator {
	opCount := operatorCount(tb)

	isHermetic, isGripmock := isHermeticTest(), IsGripmock()
	if isHermetic && isGripmock {
		tb.Fatal("Cannot set both HERMETIC_TEST and GRIPMOCK environment variables to true")
	}

	certPath := ""
	if isHermetic {
		certPath = minikubeCAFilePath
	}

	operators := make(map[string]*so.SigningOperator, opCount)
	basePort := 8535
	for i := range opCount {
		id := fmt.Sprintf("%064x", i+1) // "000…001", "000…002" …
		address := fmt.Sprintf("localhost:%d", basePort+i)
		var operatorConnectionFactory so.OperatorConnectionFactory = &DangerousTestOperatorConnectionFactoryNoVerifyTLS{}
		if isHermetic {
			address = fmt.Sprintf("dns:///%d.spark.minikube.local", i)
		}
		if isGripmock {
			operatorConnectionFactory = &DangerousTestOperatorConnectionFactoryNoTLS{}
		}

		operators[id] = &so.SigningOperator{
			ID:                        uint64(i),
			Identifier:                id,
			AddressRpc:                address,
			AddressDkg:                address,
			IdentityPublicKey:         testOperatorPubkeys[i],
			CertPath:                  certPath,
			OperatorConnectionFactory: operatorConnectionFactory,
		}
	}
	return operators
}

func getTestDatabasePath(operatorIndex int) string {
	if isHermeticTest() {
		return fmt.Sprintf("postgresql://postgres@localhost:15432/sparkoperator_%d?sslmode=disable", operatorIndex)
	}
	return fmt.Sprintf("postgresql://:@127.0.0.1:5432/sparkoperator_%d?sslmode=disable", operatorIndex)
}

func getLocalFrostSignerAddress(tb testing.TB) string {
	isHermetic, isGripmock := isHermeticTest(), IsGripmock()
	if isHermetic && isGripmock {
		tb.Fatal("Cannot set both HERMETIC_TEST and GRIPMOCK environment variables to true")
	}

	if isHermetic {
		return "localhost:9999"
	}
	if isGripmock {
		return "localhost:8535"
	}
	return "unix:///tmp/frost_0.sock"
}

func TestConfig(tb testing.TB) *so.Config {
	return SpecificOperatorTestConfig(tb, 0)
}

func SpecificOperatorTestConfig(tb testing.TB, operatorIndex int) *so.Config {
	operatorCount := operatorCount(tb)
	if operatorIndex >= operatorCount {
		tb.Fatalf("Operator index %d out of range", operatorIndex)
	}

	signingOperators := GetAllSigningOperators(tb)

	identifier := signingOperatorPrefix + strconv.Itoa(operatorIndex+1)
	opCount := len(signingOperators)
	threshold := (opCount + 2) / 2 // 1/1, 2/2, 2/3, 3/4, 3/5
	config := so.Config{
		Index:                      uint64(operatorIndex),
		Identifier:                 identifier,
		IdentityPrivateKey:         testOperatorPrivkeys[operatorIndex],
		SigningOperatorMap:         signingOperators,
		Threshold:                  uint64(threshold),
		SignerAddress:              getLocalFrostSignerAddress(tb),
		DatabasePath:               getTestDatabasePath(operatorIndex),
		FrostGRPCConnectionFactory: &TestGRPCConnectionFactory{},
		SupportedNetworks:          []common.Network{common.Regtest, common.Mainnet},
	}

	return &config
}

// TestWalletConfig returns a wallet configuration that can be used for testing.
func TestWalletConfig(tb testing.TB) *wallet.TestWalletConfig {
	identityPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(tb, err, "failed to generate identity private key")
	return TestWalletConfigWithIdentityKey(tb, identityPrivKey)
}

// TestWalletConfigWithIdentityKey returns a wallet configuration with specified identity key that can be used for testing.
func TestWalletConfigWithIdentityKey(tb testing.TB, identityPrivKey keys.Private) *wallet.TestWalletConfig {
	return TestWalletConfigWithParams(tb,
		TestWalletConfigParams{
			IdentityPrivateKey: identityPrivKey,
		})
}

// TestWalletConfigWithIdentityKeyAndCoordinator returns a wallet configuration with specified identity key that can be used for testing.
func TestWalletConfigWithIdentityKeyAndCoordinator(tb testing.TB, identityPrivKey keys.Private, coordinatorIndex int) *wallet.TestWalletConfig {
	return TestWalletConfigWithParams(tb,
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

// TestWalletConfigWithParams creates a wallet.Config suitable for tests using the provided parameters.
func TestWalletConfigWithParams(tb testing.TB, p TestWalletConfigParams) *wallet.TestWalletConfig {
	if p.CoordinatorIndex < 0 {
		p.CoordinatorIndex = 0
	}

	var privKey keys.Private
	if p.IdentityPrivateKey.IsZero() {
		var err error
		privKey, err = keys.GeneratePrivateKey()
		require.NoError(tb, err, "failed to generate identity private key")
	} else {
		privKey = p.IdentityPrivateKey
	}

	signingOperators := GetAllSigningOperators(tb)

	network := common.Regtest
	if p.Network != common.Unspecified {
		network = p.Network
	}

	coordinatorIdentifier := fmt.Sprintf("%064d", p.CoordinatorIndex+1)
	return &wallet.TestWalletConfig{
		Network:                               network,
		SigningOperators:                      signingOperators,
		CoordinatorIdentifier:                 coordinatorIdentifier,
		FrostSignerAddress:                    getLocalFrostSignerAddress(tb),
		IdentityPrivateKey:                    privKey,
		Threshold:                             3,
		SparkServiceProviderIdentityPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public(),
		UseTokenTransactionSchnorrSignatures:  p.UseTokenTransactionSchnorrSignatures,
		CoordinatorDatabaseURI:                getTestDatabasePath(p.CoordinatorIndex),
		FrostGRPCConnectionFactory:            &TestGRPCConnectionFactory{},
	}
}
