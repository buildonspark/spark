package testutil

import (
	"encoding/hex"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/wallet"
)

// GetAllSigningOperators returns all signing operator configurations.
func GetAllSigningOperators() (map[string]*so.SigningOperator, error) {
	pubkeys := []string{
		"0322ca18fc489ae25418a0e768273c2c61cabb823edfb14feb891e9bec62016510",
		"0341727a6c41b168f07eb50865ab8c397a53c7eef628ac1020956b705e43b6cb27",
		"0305ab8d485cc752394de4981f8a5ae004f2becfea6f432c9a59d5022d8764f0a6",
		"0352aef4d49439dedd798ac4aef1e7ebef95f569545b647a25338398c1247ffdea",
		"02c05c88cc8fc181b1ba30006df6a4b0597de6490e24514fbdd0266d2b9cd3d0ba",
	}

	pubkeyBytesArray := make([][]byte, len(pubkeys))
	for i, pubkey := range pubkeys {
		pubkeyBytes, err := hex.DecodeString(pubkey)
		if err != nil {
			return nil, err
		}
		pubkeyBytesArray[i] = pubkeyBytes
	}

	return map[string]*so.SigningOperator{
		"0000000000000000000000000000000000000000000000000000000000000001": {
			ID:                0,
			Identifier:        "0000000000000000000000000000000000000000000000000000000000000001",
			Address:           "localhost:8535",
			IdentityPublicKey: pubkeyBytesArray[0],
		},
		"0000000000000000000000000000000000000000000000000000000000000002": {
			ID:                1,
			Identifier:        "0000000000000000000000000000000000000000000000000000000000000002",
			Address:           "localhost:8536",
			IdentityPublicKey: pubkeyBytesArray[1],
		},
		"0000000000000000000000000000000000000000000000000000000000000003": {
			ID:                2,
			Identifier:        "0000000000000000000000000000000000000000000000000000000000000003",
			Address:           "localhost:8537",
			IdentityPublicKey: pubkeyBytesArray[2],
		},
		"0000000000000000000000000000000000000000000000000000000000000004": {
			ID:                3,
			Identifier:        "0000000000000000000000000000000000000000000000000000000000000004",
			Address:           "localhost:8538",
			IdentityPublicKey: pubkeyBytesArray[3],
		},
		"0000000000000000000000000000000000000000000000000000000000000005": {
			ID:                4,
			Identifier:        "0000000000000000000000000000000000000000000000000000000000000005",
			Address:           "localhost:8539",
			IdentityPublicKey: pubkeyBytesArray[4],
		},
	}, nil
}

func GetAllSigningOperatorsDeployed() (map[string]*so.SigningOperator, error) {
	pubkeys := []string{
		"03acd9a5a88db102730ff83dee69d69088cc4c9d93bbee893e90fd5051b7da9651",
		"02d2d103cacb1d6355efeab27637c74484e2a7459e49110c3fe885210369782e23",
		"0350f07ffc21bfd59d31e0a7a600e2995273938444447cb9bc4c75b8a895dbb853",
	}

	pubkeyBytesArray := make([][]byte, len(pubkeys))
	for i, pubkey := range pubkeys {
		pubkeyBytes, err := hex.DecodeString(pubkey)
		if err != nil {
			return nil, err
		}
		pubkeyBytesArray[i] = pubkeyBytes
	}

	return map[string]*so.SigningOperator{
		"0000000000000000000000000000000000000000000000000000000000000001": {
			ID:                0,
			Identifier:        "0000000000000000000000000000000000000000000000000000000000000001",
			Address:           "dns:///spark-0.dev.dev.sparkinfra.net",
			IdentityPublicKey: pubkeyBytesArray[0],
		},
		"0000000000000000000000000000000000000000000000000000000000000002": {
			ID:                1,
			Identifier:        "0000000000000000000000000000000000000000000000000000000000000002",
			Address:           "dns:///spark-1.dev.dev.sparkinfra.net",
			IdentityPublicKey: pubkeyBytesArray[1],
		},
		"0000000000000000000000000000000000000000000000000000000000000003": {
			ID:                2,
			Identifier:        "0000000000000000000000000000000000000000000000000000000000000003",
			Address:           "dns:///spark-2.dev.dev.sparkinfra.net",
			IdentityPublicKey: pubkeyBytesArray[2],
		},
	}, nil
}

// TestConfig returns a test configuration that can be used for testing.
func TestConfig() (*so.Config, error) {
	identityPrivateKeyBytes, err := hex.DecodeString("5eaae81bcf1fd43fbb92432b82dbafc8273bb3287b42cb4cf3c851fcee2212a5")
	if err != nil {
		return nil, err
	}

	signingOperators, err := GetAllSigningOperators()
	if err != nil {
		return nil, err
	}

	config := so.Config{
		Identifier:         "0000000000000000000000000000000000000000000000000000000000000001",
		IdentityPrivateKey: identityPrivateKeyBytes,
		SigningOperatorMap: signingOperators,
		Threshold:          3,
		SignerAddress:      "unix:///tmp/frost_0.sock",
		DatabasePath:       "postgresql://:@127.0.0.1:5432/operator_0?sslmode=disable",
	}
	return &config, nil
}

// TestWalletConfig returns a wallet configuration that can be used for testing.
func TestWalletConfig() (*wallet.Config, error) {
	identityPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil || identityPrivKey == nil {
		return nil, fmt.Errorf("failed to generate identity private key: %w", err)
	}
	return TestWalletConfigWithIdentityKey(*identityPrivKey)
}

// TestWalletConfigWithIdentityKey returns a wallet configuration with specified identity key that can be used for testing.
func TestWalletConfigWithIdentityKey(identityPrivKey secp256k1.PrivateKey) (*wallet.Config, error) {
	signingOperators, err := GetAllSigningOperators()
	if err != nil {
		return nil, err
	}
	return &wallet.Config{
		Network:              common.Regtest,
		SigningOperators:     signingOperators,
		CoodinatorIdentifier: "0000000000000000000000000000000000000000000000000000000000000001",
		FrostSignerAddress:   "unix:///tmp/frost_0.sock",
		IdentityPrivateKey:   identityPrivKey,
		Threshold:            3,
	}, nil
}

func TestWalletConfigDeployed(identityPrivKeyBytes []byte) (*wallet.Config, error) {
	identityPrivKey := secp256k1.PrivKeyFromBytes(identityPrivKeyBytes)
	if identityPrivKey == nil {
		return nil, fmt.Errorf("failed to generate identity private key")
	}
	signingOperators, err := GetAllSigningOperatorsDeployed()
	if err != nil {
		return nil, err
	}
	sspIdentityKey, err := hex.DecodeString("028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4")
	if err != nil {
		return nil, err
	}
	return &wallet.Config{
		Network:                               common.Regtest,
		SigningOperators:                      signingOperators,
		CoodinatorIdentifier:                  "0000000000000000000000000000000000000000000000000000000000000001",
		FrostSignerAddress:                    "unix:///tmp/frost_0.sock",
		IdentityPrivateKey:                    *identityPrivKey,
		Threshold:                             2,
		SparkServiceProviderIdentityPublicKey: sspIdentityKey,
	}, nil
}
