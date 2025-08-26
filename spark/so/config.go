package so

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"google.golang.org/grpc"

	"github.com/XSAM/otelsql"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/exaring/otelpgx"
	"github.com/goccy/go-yaml"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/middleware"
	"github.com/lightsparkdev/spark/so/utils"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
)

var (
	defaultPoolMinConns          = 4
	defaultPoolMaxConns          = 64
	defaultPoolMaxConnLifetime   = 5 * time.Minute
	defaultPoolMaxConnIdleTime   = 30 * time.Second
	defaultPoolHealthCheckPeriod = 15 * time.Second
	// Defaults for gRPC server behavior
	defaultGRPCServerConnectionTimeout   = 5 * time.Second
	defaultGRPCServerKeepaliveTime       = 300 * time.Second
	defaultGRPCServerKeepaliveTimeout    = 20 * time.Second
	defaultGRPCServerUnaryHandlerTimeout = 60 * time.Second
	// 0 or unset means to fall back to the default value.
	// < 0 means disable timeouts
	// > 0 means enable timeouts with the specified value
	defaultGRPCClientTimeout = -1 * time.Second
)

// Config is the configuration for the signing operator.
type Config struct {
	// Index is the index of the signing operator.
	Index uint64
	// Identifier is the identifier of the signing operator, which will be index + 1 in 32 bytes big endian hex string.
	// Used as shamir secret share identifier in DKG key shares.
	Identifier string
	// IdentityPrivateKey is the identity private key of the signing operator.
	IdentityPrivateKey keys.Private
	// SigningOperatorMap is the map of signing operators.
	SigningOperatorMap map[string]*SigningOperator
	// Threshold is the threshold for the signing operator.
	Threshold uint64
	// SignerAddress is the address of the signing operator.
	SignerAddress string
	// DatabasePath is the path to the database.
	DatabasePath string
	// IsRDS indicates if the database is an RDS instance.
	IsRDS bool
	// AuthzEnforced determines if client authorization checks are enforced
	AuthzEnforced bool
	// DKGConfig
	DKGConfig DkgConfig
	// SupportedNetworks is the list of networks supported by the signing operator.
	SupportedNetworks []common.Network
	// BitcoindConfigs are the configurations for different bitcoin nodes.
	BitcoindConfigs map[string]BitcoindConfig
	// ServerCertPath is the path to the server certificate.
	ServerCertPath string
	// ServerKeyPath is the path to the server key.
	ServerKeyPath string
	// Lrc20Configs are the configurations for different LRC20 nodes and
	// token transaction withdrawal parameters.
	Lrc20Configs map[string]Lrc20Config
	// RunDirectory is the base directory for resolving relative paths
	RunDirectory string
	// If true, return the details of the error to the client instead of just 'Internal Server Error'
	ReturnDetailedErrors bool
	// If true, return the details of the panic to the client instead of just 'Internal Server Error'
	ReturnDetailedPanicErrors bool
	// RateLimiter is the configuration for the rate limiter
	RateLimiter RateLimiterConfig
	// Tracing configuration
	Tracing common.TracingConfig
	// Database is the configuration for the database.
	Database DatabaseConfig
	// identityPubkeyToOperatorIdentifierMap maps the signing operator identity pubkeys to its corresponding identifier.
	identityPubkeyToOperatorIdentifierMap map[keys.Public]string
	// Token is the configuration for token-related settings.
	Token TokenConfig
	// ServiceAuthz specifies the enforcement of authorization checks for
	// internal APIs, as opposed to authzEnabled which gates client
	// authorization.
	ServiceAuthz ServiceAuthzConfig
	// Different load balancers may append the client IP earlier or later in the
	// X-Forwarded-For header. This configuration specifies the position from
	// the end of the header to use.
	XffClientIpPosition int
	// Knobs is the configuration for the knobs
	Knobs knobs.Config
	// FrostGRPCConnectionFactory generates Frost gRPC connections. Allows for overiding the basic implementation for testing.
	FrostGRPCConnectionFactory frost.FrostGRPCConnectionFactory
	// GRPC contains configuration for gRPC server behavior
	GRPC GRPCConfig
}

// DatabaseDriver returns the database driver based on the database path.
func (c *Config) DatabaseDriver() string {
	if strings.HasPrefix(c.DatabasePath, "postgresql") {
		return "postgres"
	}
	return "sqlite3"
}

// TokenConfig contains token-related configuration settings.
type TokenConfig struct {
	// RequireTokenIdentifierForMints determines if token mints must specify a token identifier in the outputs.
	RequireTokenIdentifierForMints bool `yaml:"require_token_identifier_for_mints"`
	// RequireTokenIdentifierForTransfers determines if token transfers must specify a token identifier in its input + outputs
	RequireTokenIdentifierForTransfers bool `yaml:"require_token_identifier_for_transfers"`
	// DisconnectLRC20Node turns on operator enforcement of announce and max supply and disables all LRC20 node communication.
	DisconnectLRC20Node bool `yaml:"disconnect_lrc20_node"`
	// DisableSparkTokenCreationForL1TokenAnnouncements disables logic creating a spark native token in response to finding a token announcement on L1.
	// We will turn this flag on to require spark native announce or L1 + cross-consensus reserve for token creation.
	DisableSparkTokenCreationForL1TokenAnnouncements bool `yaml:"disable_spark_token_creation_for_l1_token_announcements"`
	// EnableBackfillSpentTokenTransactionHistoryTask enables the backfill spent token transaction history task.
	EnableBackfillSpentTokenTransactionHistoryTask bool `yaml:"enable_backfill_spent_token_transaction_history_task"`
	// RequireThresholdOperators, when set to true, makes operator signature and finalization use
	// the configured threshold value instead of requiring responses from all operators to succeed.
	RequireThresholdOperators bool `yaml:"require_threshold_operators"`
	// EnableManualTokenTransactionFinalizeByTxHashTask enables the manual finalize-by-hash startup task.
	EnableManualTokenTransactionFinalizeByTxHashTask bool `yaml:"enable_manual_token_transaction_finalize_by_tx_hash_task"`
}

// OperatorConfig contains the configuration for a signing operator.
type OperatorConfig struct {
	Dkg DkgConfig `yaml:"dkg"`
	// Bitcoind is a map of bitcoind configurations per network.
	Bitcoind map[string]BitcoindConfig `yaml:"bitcoind"`
	// Lrc20 is a map of addresses of lrc20 nodes per network
	Lrc20 map[string]Lrc20Config `yaml:"lrc20"`
	// Tracing is the configuration for tracing
	Tracing common.TracingConfig `yaml:"tracing"`
	// Database is the configuration for the database
	Database DatabaseConfig `yaml:"database"`
	// ReturnDetailedErrors determines if detailed errors should be returned to the client
	ReturnDetailedErrors bool `yaml:"return_detailed_errors"`
	// ReturnDetailedPanicErrors determines if detailed panic errors should be returned to the client
	ReturnDetailedPanicErrors bool `yaml:"return_detailed_panic_errors"`
	// Token contains token-related configuration settings
	Token TokenConfig `yaml:"token"`
	// Configuration for authorization of internal service APIs.
	ServiceAuthz ServiceAuthzConfig `yaml:"service_authz"`
	// XffClientIPPosition specifies the position from the end of the X-Forwarded-For header to use for the client IP.
	XffClientIpPosition int `yaml:"xff_client_ip_position"`
	// Knobs is the configuration for the knobs
	Knobs knobs.Config `yaml:"knobs"`
	// RateLimiter is the configuration for the rate limiter
	RateLimiter RateLimiterConfig `yaml:"rate_limiter"`
	// GRPC holds configuration for gRPC server behavior
	GRPC GRPCConfig `yaml:"grpc"`
}

type DkgConfig struct {
	// The minimum number of available keys. If the number of available keys falls below this
	// threshold, DKG will be run to replenish the pool of available keys.
	MinAvailableKeys *int `yaml:"min_available_keys"`
}

// BitcoindConfig is the configuration for a bitcoind node.
type BitcoindConfig struct {
	Network                      string `yaml:"network"`
	Host                         string `yaml:"host"`
	User                         string `yaml:"rpcuser"`
	Password                     string `yaml:"rpcpassword"`
	ZmqPubRawBlock               string `yaml:"zmqpubrawblock"`
	DepositConfirmationThreshold uint   `yaml:"deposit_confirmation_threshold"`
	// Enable Watchtowers in Chain Watcher, this may slow down block processing
	ProcessNodesForWatchtowers *bool `yaml:"process_nodes_for_watchtowers"`
}

type Lrc20Config struct {
	// DisableRpcs turns off external LRC20 RPC calls for token transactions.
	// Useful to unblock token transactions in the case LRC20 nodes behave unexpectedly.
	// Although this is primarily intended for testing, even in a production environment
	// transfers can still be validated and processed without LRC20 communication,
	// although exits for resulting outputs will be blocked until the data is backfilled.
	DisableRpcs bool `yaml:"disablerpcs"`
	// DisableL1 removes the ability for clients to move tokens on L1.  All tokens minted in this Spark instance
	// must then stay within this spark instance. It disables SO chainwatching for withdrawals and disables L1 watchtower logic.
	// Note that it DOES NOT impact the need for announcing tokens on L1 before minting.
	// The intention is that if this config value is set in an SO- that any tokens minted do not have Unilateral Exit or L1 deposit capabilities.
	DisableL1                     bool   `yaml:"disablel1"`
	Network                       string `yaml:"network"`
	Host                          string `yaml:"host"`
	RelativeCertPath              string `yaml:"relativecertpath"`
	WithdrawBondSats              uint64 `yaml:"withdrawbondsats"`
	WithdrawRelativeBlockLocktime uint64 `yaml:"withdrawrelativeblocklocktime"`
	// TransactionExpiryDuration is the duration after which started token transactions expire
	// after which the tx will be cancelled and the input TTXOs will be reset to a spendable state.
	TransactionExpiryDuration time.Duration `yaml:"transaction_expiry_duration"`
	GRPCPageSize              uint64        `yaml:"grpcspagesize"`
	GRPCPoolSize              uint64        `yaml:"grpcpoolsize"`
}

// GRPCConfig contains configuration for gRPC server and client behavior.
// All durations support Go-style duration strings such as "5s", "3m", etc.
type GRPCConfig struct {
	// ServerConnectionTimeout controls the timeout for establishing new incoming connections.
	ServerConnectionTimeout time.Duration `yaml:"server_connection_timeout"`
	// ServerKeepaliveTime is the interval between keepalive pings.
	ServerKeepaliveTime time.Duration `yaml:"server_keepalive_time"`
	// ServerKeepaliveTimeout is the timeout waiting for keepalive ack before closing the connection.
	ServerKeepaliveTimeout time.Duration `yaml:"server_keepalive_timeout"`
	// ServerUnaryHandlerTimeout enforces a per-request timeout for unary RPC handlers.
	ServerUnaryHandlerTimeout time.Duration `yaml:"server_unary_handler_timeout"`
	// ClientTimeout enforces a per-request timeout for unary RPC client calls.
	ClientTimeout time.Duration `yaml:"client_timeout"`
}

type DatabaseConfig struct {
	PoolMinConns              *int           `yaml:"pool_min_conns"`
	PoolMaxConns              *int           `yaml:"pool_max_conns"`
	PoolMaxConnLifetime       *time.Duration `yaml:"pool_max_conn_lifetime"`
	PoolMaxConnIdleTime       *time.Duration `yaml:"pool_max_conn_idle_time"`
	PoolHealthCheckPeriod     *time.Duration `yaml:"pool_health_check_period"`
	PoolMaxConnLifetimeJitter *time.Duration `yaml:"pool_max_conn_lifetime_jitter"`
	NewTxTimeout              *time.Duration `yaml:"new_tx_timeout"`
}

// RateLimiterConfig is the configuration for the rate limiter
type RateLimiterConfig struct {
	// Enabled determines if rate limiting is enabled
	Enabled bool `yaml:"enabled"`
	// Window is the time window for rate limiting
	Window time.Duration `yaml:"window"`
	// MaxRequests is the maximum number of requests allowed in the window
	MaxRequests int `yaml:"max_requests"`
	// Methods is a list of methods to rate limit
	// Note: This does not set up rate limiting across methods by IP,
	// nor does it provide configuration for custom per-method rate limiting.
	Methods []string `yaml:"methods"`
}

// The authzEnabled field currently gates authorization enforcement for client
// bearer tokens. This configuration gates access to sensitive internal APIs.
type ServiceAuthzConfig struct {
	// Mode specifies whether to enforce, warn, or log authorization
	// checks.
	Mode authz.Mode `yaml:"mode"`
	// IPAllowlist is the list of IP addresses that are allowed privileged
	// access to the SOs.
	IPAllowlist []string `yaml:"ip_allowlist"`
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
	isRDS bool,
	authzEnforced bool,
	supportedNetworks []common.Network,
	serverCertPath string,
	serverKeyPath string,
	runDirectory string,
	rateLimiter RateLimiterConfig,
) (*Config, error) {
	identityPrivateKeyHexStringBytes, err := os.ReadFile(identityPrivateKeyFilePath)
	if err != nil {
		return nil, err
	}
	identityPrivateKeyBytes, err := hex.DecodeString(strings.TrimSpace(string(identityPrivateKeyHexStringBytes)))
	if err != nil {
		return nil, err
	}
	identityPrivateKey, err := keys.ParsePrivateKey(identityPrivateKeyBytes)
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

	var operatorConfig OperatorConfig
	if err := yaml.Unmarshal(data, &operatorConfig); err != nil {
		return nil, err
	}

	setLrc20Defaults(operatorConfig.Lrc20)
	setGrpcDefaults(&operatorConfig.GRPC)

	if index > math.MaxUint32 {
		return nil, fmt.Errorf("invalid index: %d exceeds %d", index, math.MaxUint32)
	}
	identifier := utils.IndexToIdentifier(uint32(index))

	if !operatorConfig.ServiceAuthz.Mode.Valid() {
		slog.Warn("unset or invalid authz mode - treating authz as disabled", "mode", operatorConfig.ServiceAuthz.Mode)
		operatorConfig.ServiceAuthz.Mode = authz.ModeLogOnly
	} else if operatorConfig.ServiceAuthz.Mode == authz.ModeDisabled {
		slog.Info("authz mode is disabled - no service authorization checks will be performed")
	} else {
		slog.Info("authz mode set", "mode", operatorConfig.ServiceAuthz.Mode)
	}

	if len(operatorConfig.ServiceAuthz.IPAllowlist) == 0 {
		slog.Warn("no IP allowlist specified for authz")
		operatorConfig.ServiceAuthz.IPAllowlist = []string{}
	} else if operatorConfig.ServiceAuthz.Mode == authz.ModeDisabled {
		slog.Warn("authz mode is disable, but IP allowlist is set - potential misconfiguration")
	} else {
		slog.Info("authz ip allowlist set", "allowlist", operatorConfig.ServiceAuthz.IPAllowlist)
	}

	if operatorConfig.XffClientIpPosition < 0 {
		slog.Warn("xff_client_ip_position is negative - using default value", "default_position", 0)
		operatorConfig.XffClientIpPosition = 0
	}

	// We need to be able to set the rate limiter both from the command line and
	// from the operator config. If either says "enable", then we enable. Then
	// we take the values from whichever said "enable." If they both say
	// "enable," then use the operator config as the final override.
	if operatorConfig.RateLimiter.Enabled {
		rateLimiter = operatorConfig.RateLimiter
	}

	conf := &Config{
		Index:                      index,
		Identifier:                 identifier,
		IdentityPrivateKey:         identityPrivateKey,
		SigningOperatorMap:         signingOperatorMap,
		Threshold:                  threshold,
		SignerAddress:              signerAddress,
		DatabasePath:               databasePath,
		IsRDS:                      isRDS,
		AuthzEnforced:              authzEnforced,
		DKGConfig:                  operatorConfig.Dkg,
		SupportedNetworks:          supportedNetworks,
		BitcoindConfigs:            operatorConfig.Bitcoind,
		Lrc20Configs:               operatorConfig.Lrc20,
		ServerCertPath:             serverCertPath,
		ServerKeyPath:              serverKeyPath,
		RunDirectory:               runDirectory,
		ReturnDetailedErrors:       operatorConfig.ReturnDetailedErrors,
		ReturnDetailedPanicErrors:  operatorConfig.ReturnDetailedPanicErrors,
		RateLimiter:                rateLimiter,
		Tracing:                    operatorConfig.Tracing,
		Database:                   operatorConfig.Database,
		Token:                      operatorConfig.Token,
		ServiceAuthz:               operatorConfig.ServiceAuthz,
		XffClientIpPosition:        operatorConfig.XffClientIpPosition,
		Knobs:                      operatorConfig.Knobs,
		FrostGRPCConnectionFactory: &frost.FrostGRPCConnectionFactorySecure{},
		GRPC:                       operatorConfig.GRPC,
	}

	conf.buildIdentityPubkeyMap()
	return conf, nil
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

var OtelSQLSpanOptions = otelsql.SpanOptions{
	OmitConnResetSession: true,
	OmitConnPrepare:      true,
}

type DBConnector struct {
	uri    *url.URL
	isRDS  bool
	driver driver.Driver
	pool   *pgxpool.Pool
}

func getDatabaseStatementTimeoutMs(k knobs.Knobs) uint64 {
	return uint64(k.GetValue(knobs.KnobDatabaseStatementTimeout, 60) * 1000)
}

func NewDBConnector(ctx context.Context, soConfig *Config, knobsService knobs.Knobs) (*DBConnector, error) {
	uri, err := url.Parse(soConfig.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database path: %w", err)
	}

	otelWrappedDriver := otelsql.WrapDriver(stdlib.GetDefaultDriver(),
		otelsql.WithAttributes(semconv.DBSystemPostgreSQL),
		otelsql.WithSpanOptions(OtelSQLSpanOptions),
	)

	connector := &DBConnector{
		uri:    uri,
		isRDS:  soConfig.IsRDS,
		driver: otelWrappedDriver,
	}

	// Only create pool for PostgreSQL
	if strings.HasPrefix(soConfig.DatabasePath, "postgresql") {
		conf, err := pgxpool.ParseConfig(soConfig.DatabasePath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse pool config: %w", err)
		}
		conf.ConnConfig.Tracer = otelpgx.NewTracer()

		conf.MinConns = int32(defaultPoolMinConns)
		if soConfig.Database.PoolMinConns != nil {
			conf.MinConns = int32(*soConfig.Database.PoolMinConns)
		}

		conf.MaxConns = int32(defaultPoolMaxConns)
		if soConfig.Database.PoolMaxConns != nil {
			conf.MaxConns = int32(*soConfig.Database.PoolMaxConns)
		}

		conf.MaxConnLifetime = defaultPoolMaxConnLifetime
		if soConfig.Database.PoolMaxConnLifetime != nil {
			conf.MaxConnLifetime = *soConfig.Database.PoolMaxConnLifetime
		}

		conf.MaxConnIdleTime = defaultPoolMaxConnIdleTime
		if soConfig.Database.PoolMaxConnIdleTime != nil {
			conf.MaxConnIdleTime = *soConfig.Database.PoolMaxConnIdleTime
		}

		conf.HealthCheckPeriod = defaultPoolHealthCheckPeriod
		if soConfig.Database.PoolHealthCheckPeriod != nil {
			conf.HealthCheckPeriod = *soConfig.Database.PoolHealthCheckPeriod
		}

		if soConfig.Database.PoolMaxConnLifetimeJitter != nil {
			conf.MaxConnLifetimeJitter = *soConfig.Database.PoolMaxConnLifetimeJitter
		}

		if soConfig.IsRDS {
			conf.BeforeConnect = func(ctx context.Context, cfg *pgx.ConnConfig) error {
				token, err := NewRDSAuthToken(ctx, uri)
				if err != nil {
					return fmt.Errorf("failed to get RDS auth token: %w", err)
				}
				cfg.Password = token
				return nil
			}
		}

		if podName, ok := os.LookupEnv("POD_NAME"); ok {
			conf.ConnConfig.RuntimeParams["application_name"] = podName
		}

		conf.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			statementTimeoutMs := getDatabaseStatementTimeoutMs(knobsService)
			_, err := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = %d", statementTimeoutMs))
			if err != nil {
				return fmt.Errorf("failed to set statement_timeout: %w", err)
			}
			return nil
		}

		pool, err := pgxpool.NewWithConfig(ctx, conf)
		if err != nil {
			return nil, fmt.Errorf("failed to create connection pool: %w", err)
		}

		err = otelpgx.RecordStats(pool)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize stats for connection pool: %w", err)
		}

		connector.pool = pool
	}

	return connector, nil
}

func (c *DBConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if !c.isRDS {
		return c.driver.Open(c.uri.String())
	}
	uri := c.uri
	token, err := NewRDSAuthToken(ctx, c.uri)
	if err != nil {
		return nil, err
	}
	uri.User = url.UserPassword(uri.User.Username(), token)
	return c.driver.Open(uri.String())
}

func (c *DBConnector) Driver() driver.Driver {
	return c.driver
}

func (c *DBConnector) Pool() *pgxpool.Pool {
	return c.pool
}

func (c *DBConnector) Close() {
	if c.pool != nil {
		c.pool.Close()
	}
}

// LoadOperators loads the operators from the given file path.
func LoadOperators(filePath string) (map[string]*SigningOperator, error) {
	operators := make(map[string]*SigningOperator)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var yamlObj any
	if err := yaml.Unmarshal(data, &yamlObj); err != nil {
		return nil, err
	}

	jsonStr, err := json.Marshal(yamlObj)
	if err != nil {
		return nil, err
	}

	var operatorList []*SigningOperator
	if err := json.Unmarshal(jsonStr, &operatorList); err != nil {
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

func (c *Config) buildIdentityPubkeyMap() {
	c.identityPubkeyToOperatorIdentifierMap = make(map[keys.Public]string, len(c.SigningOperatorMap))
	for _, operator := range c.SigningOperatorMap {
		c.identityPubkeyToOperatorIdentifierMap[operator.IdentityPublicKey] = operator.Identifier
	}
}

func (c *Config) GetOperatorIdentifierFromIdentityPublicKey(identityPublicKey keys.Public) string {
	if len(c.identityPubkeyToOperatorIdentifierMap) == 0 {
		c.buildIdentityPubkeyMap()
	}
	return c.identityPubkeyToOperatorIdentifierMap[identityPublicKey]
}

// IsAuthzEnforced returns whether authorization is enforced
func (c *Config) IsAuthzEnforced() bool {
	return c.AuthzEnforced
}

func (c *Config) IdentityPublicKey() keys.Public {
	return c.SigningOperatorMap[c.Identifier].IdentityPublicKey
}

func (c *Config) GetRateLimiterConfig() *middleware.RateLimiterConfig {
	return &middleware.RateLimiterConfig{
		Window:              c.RateLimiter.Window,
		MaxRequests:         c.RateLimiter.MaxRequests,
		Methods:             c.RateLimiter.Methods,
		XffClientIpPosition: c.XffClientIpPosition,
	}
}

const (
	defaultTokenTransactionExpiryDuration = 3 * time.Minute
)

// setLrc20Defaults sets default values for Lrc20Config fields if they are zero.
func setLrc20Defaults(lrc20Configs map[string]Lrc20Config) {
	for k, v := range lrc20Configs {
		if v.TransactionExpiryDuration == 0 {
			slog.Info("TokenTransactionExpiryDuration not set, using default value", "default_duration", defaultTokenTransactionExpiryDuration)
			v.TransactionExpiryDuration = defaultTokenTransactionExpiryDuration
		}
		lrc20Configs[k] = v
	}
}

func (c *Config) NewFrostGRPCConnection() (*grpc.ClientConn, error) {
	return c.FrostGRPCConnectionFactory.NewFrostGRPCConnection(c.SignerAddress)
}

// setGrpcDefaults sets default values for GRPCConfig fields if they are zero.
func setGrpcDefaults(cfg *GRPCConfig) {
	if cfg == nil {
		return
	}
	if cfg.ServerConnectionTimeout == 0 {
		cfg.ServerConnectionTimeout = defaultGRPCServerConnectionTimeout
	}
	if cfg.ServerKeepaliveTime == 0 {
		cfg.ServerKeepaliveTime = defaultGRPCServerKeepaliveTime
	}
	if cfg.ServerKeepaliveTimeout == 0 {
		cfg.ServerKeepaliveTimeout = defaultGRPCServerKeepaliveTimeout
	}
	if cfg.ServerUnaryHandlerTimeout == 0 {
		cfg.ServerUnaryHandlerTimeout = defaultGRPCServerUnaryHandlerTimeout
	}
	if cfg.ClientTimeout == 0 {
		cfg.ClientTimeout = defaultGRPCClientTimeout
	}
}
