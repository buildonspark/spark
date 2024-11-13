package so

import (
	"encoding/hex"
	"encoding/json"
	"os"

	"github.com/lightsparkdev/spark-go/so/utils"
)

type Config struct {
	Identifier         string
	IdentityPrivateKey []byte
	SigningOperatorMap map[string]*SigningOperator
	Threshold          uint64
	SignerAddress      string
}

func NewConfig(index uint64, identityPrivateKey string, operatorsFilePath string, threshold uint64, signerAddress string) (*Config, error) {
	identityPrivateKeyBytes, err := hex.DecodeString(identityPrivateKey)
	if err != nil {
		return nil, err
	}

	signingOperatorMap, err := LoadOperators(operatorsFilePath)
	if err != nil {
		return nil, err
	}

	return &Config{
		Identifier:         utils.IndexToIdentifier(index),
		IdentityPrivateKey: identityPrivateKeyBytes,
		SigningOperatorMap: signingOperatorMap,
		Threshold:          threshold,
		SignerAddress:      signerAddress,
	}, nil
}

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
