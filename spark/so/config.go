package so

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"

	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/utils"
)

// Config is the configuration for the signing operator.
type Config struct {
	// Index is the index of the signing operator.
	Index uint64
	// Identifier is the identifier of the signing operator, which will be index + 1 in 32 bytes big endian hex string.
	// Used as shamir secret share identifier in DKG key shares.
	Identifier string
	// IdentityPrivateKey is the identity private key of the signing operator.
	IdentityPrivateKey []byte
	// SigningOperatorMap is the map of signing operators.
	SigningOperatorMap map[string]*SigningOperator
	// Threshold is the threshold for the signing operator.
	Threshold uint64
	// SignerAddress is the address of the signing operator.
	SignerAddress string
	// DatabasePath is the path to the database.
	DatabasePath string
	// Network is the network of the signing operator.
	Network common.Network
	// AuthzEnforced determines if authorization checks are enforced
	AuthzEnforced bool
}

// DatabaseDriver returns the database driver based on the database path.
func (c *Config) DatabaseDriver() string {
	if strings.HasPrefix(c.DatabasePath, "postgresql") {
		return "postgres"
	}
	return "sqlite3"
}

// NewConfig creates a new config for the signing operator.
func NewConfig(index uint64, identityPrivateKey string, operatorsFilePath string, threshold uint64, signerAddress string, databasePath string, authzEnforced bool) (*Config, error) {
	identityPrivateKeyBytes, err := hex.DecodeString(identityPrivateKey)
	if err != nil {
		return nil, err
	}

	signingOperatorMap, err := LoadOperators(operatorsFilePath)
	if err != nil {
		return nil, err
	}

	return &Config{
		Index:              index,
		Identifier:         utils.IndexToIdentifier(index),
		IdentityPrivateKey: identityPrivateKeyBytes,
		SigningOperatorMap: signingOperatorMap,
		Threshold:          threshold,
		SignerAddress:      signerAddress,
		DatabasePath:       databasePath,
		Network:            common.Regtest, // TODO: load this from args
		AuthzEnforced:      authzEnforced,
	}, nil
}

// LoadOperators loads the operators from the given file path.
func LoadOperators(filePath string) (map[string]*SigningOperator, error) {
	operators := make(map[string]*SigningOperator)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var operatorList []*SigningOperator
	if err := json.Unmarshal(data, &operatorList); err != nil {
		return nil, err
	}

	for _, operator := range operatorList {
		operators[operator.Identifier] = operator
	}
	return operators, nil
}

// GetSigningOperatorList returns the list of signing operators.
func (c *Config) GetSigningOperatorList() map[string]*pb.SigningOperatorInfo {
	operatorList := make(map[string]*pb.SigningOperatorInfo)
	for _, operator := range c.SigningOperatorMap {
		operatorList[operator.Identifier] = operator.MarshalProto()
	}
	return operatorList
}
