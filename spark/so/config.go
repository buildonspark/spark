package so

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/utils"
	"gopkg.in/yaml.v3"
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
	// authzEnforced determines if authorization checks are enforced
	authzEnforced bool
	// DKGCoordinatorAddress is the address of the DKG coordinator.
	DKGCoordinatorAddress string
	// SupportedNetworks is the list of networks supported by the signing operator.
	SupportedNetworks []common.Network
	// BitcoindConfigs are the configurations for different bitcoin nodes.
	BitcoindConfigs map[string]BitcoindConfig
	// AWS determines if the database is in AWS RDS.
	AWS bool
	// ServerCertPath is the path to the server certificate.
	ServerCertPath string
	// ServerKeyPath is the path to the server key.
	ServerKeyPath string
	// Lrc20Configs are the configurations for different LRC20 nodes
	Lrc20Configs map[string]Lrc20Config
	// DKGLimitOverride is the override for the DKG limit.
	DKGLimitOverride uint64
}

// DatabaseDriver returns the database driver based on the database path.
func (c *Config) DatabaseDriver() string {
	if strings.HasPrefix(c.DatabasePath, "postgresql") {
		return "postgres"
	}
	return "sqlite3"
}

// NodesConfig is a map of bitcoind and lrc20 configs per network.
type NodesConfig struct {
	// Bitcoind is a map of bitcoind configurations per network.
	Bitcoind map[string]BitcoindConfig `yaml:"bitcoind"`
	// Lrc20 is a map of addresses of lrc20 nodes per network
	Lrc20 map[string]Lrc20Config `yaml:"lrc20"`
}

// BitcoindConfig is the configuration for a bitcoind node.
type BitcoindConfig struct {
	Network        string `yaml:"network"`
	Host           string `yaml:"host"`
	User           string `yaml:"rpcuser"`
	Password       string `yaml:"rpcpassword"`
	ZmqPubRawBlock string `yaml:"zmqpubrawblock"`
}

type Lrc20Config struct {
	Network string `yaml:"network"`
	Host    string `yaml:"host"`
}

// NewConfig creates a new config for the signing operator.
func NewConfig(
	configFilePath string,
	index uint64,
	identityPrivateKeyFilePath string,
	operatorsFilePath string,
	threshold uint64,
	signerAddress string,
	databasePath string,
	authzEnforced bool,
	dkgCoordinatorAddress string,
	supportedNetworks []common.Network,
	aws bool,
	serverCertPath string,
	serverKeyPath string,
	dkgLimitOverride uint64,
) (*Config, error) {
	identityPrivateKeyHexStringBytes, err := os.ReadFile(identityPrivateKeyFilePath)
	if err != nil {
		return nil, err
	}
	identityPrivateKeyBytes, err := hex.DecodeString(strings.TrimSpace(string(identityPrivateKeyHexStringBytes)))
	if err != nil {
		return nil, err
	}

	signingOperatorMap, err := LoadOperators(operatorsFilePath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, err
	}

	var nodes NodesConfig
	if err := yaml.Unmarshal(data, &nodes); err != nil {
		return nil, err
	}

	log.Printf("Server cert path: %s", serverCertPath)
	log.Printf("Server key path: %s", serverKeyPath)

	identifier := utils.IndexToIdentifier(index)

	if dkgCoordinatorAddress == "" {
		dkgCoordinatorAddress = signingOperatorMap[identifier].Address
	}

	return &Config{
		Index:                 index,
		Identifier:            identifier,
		IdentityPrivateKey:    identityPrivateKeyBytes,
		SigningOperatorMap:    signingOperatorMap,
		Threshold:             threshold,
		SignerAddress:         signerAddress,
		DatabasePath:          databasePath,
		authzEnforced:         authzEnforced,
		DKGCoordinatorAddress: dkgCoordinatorAddress,
		SupportedNetworks:     supportedNetworks,
		BitcoindConfigs:       nodes.Bitcoind,
		Lrc20Configs:          nodes.Lrc20,
		AWS:                   aws,
		ServerCertPath:        serverCertPath,
		ServerKeyPath:         serverKeyPath,
		DKGLimitOverride:      dkgLimitOverride,
	}, nil
}

func (c *Config) IsNetworkSupported(network common.Network) bool {
	for _, supportedNetwork := range c.SupportedNetworks {
		if supportedNetwork == network {
			return true
		}
	}
	return false
}

func NewRDSAuthToken(ctx context.Context, uri *url.URL) (string, error) {
	awsRegion := os.Getenv("AWS_REGION")
	if awsRegion == "" {
		return "", fmt.Errorf("AWS_REGION is not set")
	}
	awsRoleArn := os.Getenv("AWS_ROLE_ARN")
	if awsRoleArn == "" {
		return "", fmt.Errorf("AWS_ROLE_ARN is not set")
	}
	awsWebIdentityTokenFile := os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")
	if awsWebIdentityTokenFile == "" {
		return "", fmt.Errorf("AWS_WEB_IDENTITY_TOKEN_FILE is not set")
	}
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		return "", fmt.Errorf("POD_NAME is not set")
	}

	dbUser := uri.User.Username()
	dbEndpoint := uri.Host

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", err
	}

	client := sts.NewFromConfig(cfg)
	awsCreds := aws.NewCredentialsCache(stscreds.NewWebIdentityRoleProvider(
		client,
		awsRoleArn,
		stscreds.IdentityTokenFile(awsWebIdentityTokenFile),
		func(o *stscreds.WebIdentityRoleOptions) {
			o.RoleSessionName = podName
		}))

	token, err := auth.BuildAuthToken(ctx, dbEndpoint, awsRegion, dbUser, awsCreds)
	if err != nil {
		return "", err
	}

	return token, nil
}

// DBConnector is a database connector that can be configured
// to generate a new AWS RDS auth token for each connection.
type DBConnector struct {
	baseURI *url.URL
	AWS     bool
	driver  driver.Driver
}

func NewDBConnector(urlStr string, aws bool) (*DBConnector, error) {
	uri, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database path: %w", err)
	}
	return &DBConnector{
		baseURI: uri,
		AWS:     aws,
		driver:  stdlib.GetDefaultDriver(),
	}, nil
}

func (c *DBConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if !c.AWS {
		return c.driver.Open(c.baseURI.String())
	}
	uri := c.baseURI
	token, err := NewRDSAuthToken(ctx, c.baseURI)
	if err != nil {
		return nil, err
	}
	uri.User = url.UserPassword(uri.User.Username(), token)
	return c.driver.Open(uri.String())
}

// Driver returns the underlying driver.
func (c *DBConnector) Driver() driver.Driver {
	return c.driver
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

// AuthzEnforced returns whether authorization is enforced
func (c *Config) AuthzEnforced() bool {
	return c.authzEnforced
}

func (c *Config) IdentityPublicKey() []byte {
	return c.SigningOperatorMap[c.Identifier].IdentityPublicKey
}
