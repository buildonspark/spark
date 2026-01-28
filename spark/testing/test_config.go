package sparktesting

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"google.golang.org/grpc"

	"github.com/lightsparkdev/spark/common"
	sparkgrpc "github.com/lightsparkdev/spark/common/grpc"
	"github.com/lightsparkdev/spark/so"
)

const (
	minikubeCAFilePath               = "/tmp/minikube-ca.pem"
	minikubeDefaultNumSparkOperators = 3
	signingOperatorPrefix            = "000000000000000000000000000000000000000000000000000000000000000"
)

func IsMinikube() bool {
	return os.Getenv("MINIKUBE_IP") != ""
}

func GetMinikubeIP() string {
	return os.Getenv("MINIKUBE_IP")
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

	if IsMinikube() {
		return minikubeDefaultNumSparkOperators
	}

	// Otherwise, default to the maximum number of available test operator keys.
	return len(testOperatorPubkeys)
}

func GetAllSigningOperators(tb testing.TB) map[string]*so.SigningOperator {
	opCount := operatorCount(tb)

	isMinikube, isGripmock := IsMinikube(), IsGripmock()
	if isMinikube && isGripmock {
		tb.Fatal("Cannot set both MINIKUBE_IP and GRIPMOCK environment variables")
	}

	certPath := ""
	if isMinikube {
		certPath = minikubeCAFilePath
	}

	operators := make(map[string]*so.SigningOperator, opCount)
	basePort := 8535
	for i := range opCount {
		id := fmt.Sprintf("%064x", i+1) // "000…001", "000…002" …
		address := fmt.Sprintf("localhost:%d", basePort+i)
		var operatorConnectionFactory so.OperatorConnectionFactory = &DangerousTestOperatorConnectionFactoryNoVerifyTLS{}
		if isMinikube {
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

func GetTestDatabasePath(operatorIndex int) string {
	if IsMinikube() {
		return fmt.Sprintf("postgresql://postgres@%s/sparkoperator_%d?sslmode=disable",
			net.JoinHostPort(GetMinikubeIP(), "5432"),
			operatorIndex,
		)
	}
	return fmt.Sprintf("postgresql://:@127.0.0.1:5432/sparkoperator_%d?sslmode=disable", operatorIndex)
}

func GetLocalFrostSignerAddress(tb testing.TB) string {
	isMinikube, isGripmock := IsMinikube(), IsGripmock()
	if isMinikube && isGripmock {
		tb.Fatal("Cannot set both MINIKUBE_IP and GRIPMOCK environment variables")
	}

	if isMinikube {
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
	dbEventsEnabled := true
	config := so.Config{
		Index:                      uint64(operatorIndex),
		Identifier:                 identifier,
		IdentityPrivateKey:         testOperatorPrivkeys[operatorIndex],
		SigningOperatorMap:         signingOperators,
		Threshold:                  uint64(threshold),
		SignerAddress:              GetLocalFrostSignerAddress(tb),
		DatabasePath:               GetTestDatabasePath(operatorIndex),
		FrostGRPCConnectionFactory: &TestGRPCConnectionFactory{},
		SupportedNetworks:          []btcnetwork.Network{btcnetwork.Regtest, btcnetwork.Mainnet},
	}
	config.Database.DBEventsEnabled = &dbEventsEnabled

	return &config
}
