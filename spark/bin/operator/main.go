package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/go-co-op/gocron/v2"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	_ "github.com/lib/pq"
	"github.com/lightsparkdev/spark-go/common"
	pbdkg "github.com/lightsparkdev/spark-go/proto/dkg"
	pbspark "github.com/lightsparkdev/spark-go/proto/spark"
	pbauthn "github.com/lightsparkdev/spark-go/proto/spark_authn"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	pbtree "github.com/lightsparkdev/spark-go/proto/spark_tree"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authn"
	"github.com/lightsparkdev/spark-go/so/authninternal"
	"github.com/lightsparkdev/spark-go/so/chain"
	"github.com/lightsparkdev/spark-go/so/dkg"
	"github.com/lightsparkdev/spark-go/so/ent"
	sparkgrpc "github.com/lightsparkdev/spark-go/so/grpc"
	"github.com/lightsparkdev/spark-go/so/task"
	_ "github.com/mattn/go-sqlite3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

type args struct {
	LogLevel                   string
	LogJSON                    bool
	ConfigFilePath             string
	Index                      uint64
	IdentityPrivateKeyFilePath string
	OperatorsFilePath          string
	Threshold                  uint64
	SignerAddress              string
	Port                       uint64
	DatabasePath               string
	MockOnchain                bool
	ChallengeTimeout           time.Duration
	SessionDuration            time.Duration
	AuthzEnforced              bool
	DKGCoordinatorAddress      string
	DisableDKG                 bool
	SupportedNetworks          string
	AWS                        bool
	ServerCertPath             string
	ServerKeyPath              string
}

func (a *args) SupportedNetworksList() []common.Network {
	networks := make([]common.Network, 0)
	if strings.Contains(a.SupportedNetworks, "mainnet") || a.SupportedNetworks == "" {
		networks = append(networks, common.Mainnet)
	}
	if strings.Contains(a.SupportedNetworks, "testnet") || a.SupportedNetworks == "" {
		networks = append(networks, common.Testnet)
	}
	if strings.Contains(a.SupportedNetworks, "regtest") || a.SupportedNetworks == "" {
		networks = append(networks, common.Regtest)
	}
	if strings.Contains(a.SupportedNetworks, "signet") || a.SupportedNetworks == "" {
		networks = append(networks, common.Signet)
	}
	return networks
}

func loadArgs() (*args, error) {
	args := &args{}

	// Define flags
	flag.StringVar(&args.LogLevel, "log-level", "debug", "Logging level: debug|info|warn|error")
	flag.BoolVar(&args.LogJSON, "log-json", false, "Output logs in JSON format")
	flag.StringVar(&args.ConfigFilePath, "config", "so_config.yaml", "Path to config file")
	flag.Uint64Var(&args.Index, "index", 0, "Index value")
	flag.StringVar(&args.IdentityPrivateKeyFilePath, "key", "", "Identity private key")
	flag.StringVar(&args.OperatorsFilePath, "operators", "", "Path to operators file")
	flag.Uint64Var(&args.Threshold, "threshold", 0, "Threshold value")
	flag.StringVar(&args.SignerAddress, "signer", "", "Signer address")
	flag.Uint64Var(&args.Port, "port", 0, "Port value")
	flag.StringVar(&args.DatabasePath, "database", "", "Path to database file")
	flag.BoolVar(&args.MockOnchain, "mock-onchain", false, "Mock onchain tx")
	flag.DurationVar(&args.ChallengeTimeout, "challenge-timeout", time.Duration(time.Minute), "Challenge timeout")
	flag.DurationVar(&args.SessionDuration, "session-duration", time.Duration(time.Minute*15), "Session duration")
	flag.BoolVar(&args.AuthzEnforced, "authz-enforced", false, "Enforce authorization checks")
	flag.StringVar(&args.DKGCoordinatorAddress, "dkg-address", "", "DKG coordinator address")
	flag.BoolVar(&args.DisableDKG, "disable-dkg", false, "Disable DKG")
	flag.StringVar(&args.SupportedNetworks, "supported-networks", "", "Supported networks")
	flag.BoolVar(&args.AWS, "aws", false, "Use AWS RDS")
	flag.StringVar(&args.ServerCertPath, "server-cert", "", "Path to server certificate")
	flag.StringVar(&args.ServerKeyPath, "server-key", "", "Path to server key")
	// Parse flags
	flag.Parse()

	var level slog.Level
	switch strings.ToLower(args.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, errors.New("Invalid log level")
	}

	options := slog.HandlerOptions{AddSource: true, Level: level}
	var handler slog.Handler
	if args.LogJSON {
		handler = slog.NewJSONHandler(os.Stdout, &options)
	} else {
		handler = slog.NewTextHandler(os.Stdout, &options)
	}
	slog.SetDefault(slog.New(handler))

	if args.IdentityPrivateKeyFilePath == "" {
		return nil, errors.New("identity private key file path is required")
	}

	if args.OperatorsFilePath == "" {
		return nil, errors.New("operators file is required")
	}

	if args.SignerAddress == "" {
		return nil, errors.New("signer address is required")
	}

	if args.Port == 0 {
		return nil, errors.New("port is required")
	}

	if args.DatabasePath == "" {
		return nil, errors.New("database path is required")
	}

	if args.ServerCertPath == "" || args.ServerKeyPath == "" {
		return nil, errors.New("server certificate and key path are required")
	}

	return args, nil
}

func main() {
	args, err := loadArgs()
	if err != nil {
		log.Fatalf("Failed to load args: %v", err)
	}

	config, err := so.NewConfig(
		args.ConfigFilePath,
		args.Index,
		args.IdentityPrivateKeyFilePath,
		args.OperatorsFilePath, // TODO: Refactor this into the yaml config
		args.Threshold,
		args.SignerAddress,
		args.DatabasePath,
		args.AuthzEnforced,
		args.DKGCoordinatorAddress,
		args.SupportedNetworksList(),
		args.AWS,
		args.ServerCertPath,
		args.ServerKeyPath,
	)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}

	ctx := context.Background()

	dbDriver := config.DatabaseDriver()
	connector, err := so.NewDBConnector(config.DatabasePath, config.AWS)
	if err != nil {
		log.Fatalf("Failed to create db connector: %v", err)
	}
	db := sql.OpenDB(connector)
	dialectDriver := entsql.NewDriver(dbDriver, entsql.Conn{ExecQuerier: db})
	dbClient := ent.NewClient(ent.Driver(dialectDriver))
	defer dbClient.Close()

	if dbDriver == "sqlite3" {
		sqliteDb, _ := sql.Open("sqlite3", config.DatabasePath)
		if _, err := sqliteDb.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
			log.Fatalf("Failed to set journal_mode: %v", err)
		}
		if _, err := sqliteDb.ExecContext(ctx, "PRAGMA busy_timeout=5000;"); err != nil {
			log.Fatalf("Failed to set busy_timeout: %v", err)
		}
		sqliteDb.Close()
	}

	if err := dbClient.Schema.Create(ctx); err != nil {
		log.Fatalf("failed creating schema resources: %v", err)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", args.Port))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", args.Port, err)
	}

	frostConnection, err := common.NewGRPCConnectionWithoutTLS(args.SignerAddress)
	if err != nil {
		log.Fatalf("Failed to create frost client: %v", err)
	}

	s, err := gocron.NewScheduler()
	if err != nil {
		log.Fatalf("Failed to create scheduler: %v", err)
	}

	for network, bitcoindConfig := range config.BitcoindConfigs {
		go func() {
			err := chain.WatchChain(dbClient, bitcoindConfig)
			if err != nil {
				log.Fatalf("Failed to watch %s chain: %v", network, err)
			}
		}()
		log.Printf("Watching %s chain\n", network)
	}

	for _, task := range task.AllTasks() {
		_, err := s.NewJob(gocron.DurationJob(task.Duration), gocron.NewTask(task.Task, config, dbClient))
		if err != nil {
			log.Fatalf("Failed to create job: %v", err)
		}
	}

	s.Start()

	sessionTokenCreatorVerifier, err := authninternal.NewSessionTokenCreatorVerifier(config.IdentityPrivateKey, nil)
	if err != nil {
		log.Fatalf("Failed to create token verifier: %v", err)
	}

	cert, err := tls.LoadX509KeyPair(args.ServerCertPath, args.ServerKeyPath)
	if err != nil {
		log.Fatalf("Failed to load server certificate: %v", err)
	}
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.NoClientCert,
		MinVersion:   tls.VersionTLS12,
	})
	log.Printf("Server starting with TLS on: %v", args.ServerCertPath)

	grpcServer := grpc.NewServer(
		grpc.Creds(creds),
		grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
			ent.DbSessionMiddleware(dbClient),
			authn.NewAuthnInterceptor(sessionTokenCreatorVerifier).AuthnInterceptor,
		)),
	)

	if !args.DisableDKG {
		dkgServer := dkg.NewServer(frostConnection, config)
		pbdkg.RegisterDKGServiceServer(grpcServer, dkgServer)
	}

	sparkInternalServer := sparkgrpc.NewSparkInternalServer(config)
	pbinternal.RegisterSparkInternalServiceServer(grpcServer, sparkInternalServer)

	sparkServer := sparkgrpc.NewSparkServer(config)
	pbspark.RegisterSparkServiceServer(grpcServer, sparkServer)

	treeServer := sparkgrpc.NewSparkTreeServer(config, dbClient)
	pbtree.RegisterSparkTreeServiceServer(grpcServer, treeServer)

	authnServer, err := sparkgrpc.NewAuthnServer(sparkgrpc.AuthnServerConfig{
		IdentityPrivateKey: config.IdentityPrivateKey,
		ChallengeTimeout:   args.ChallengeTimeout,
		SessionDuration:    args.SessionDuration,
	}, sessionTokenCreatorVerifier)
	if err != nil {
		log.Fatalf("Failed to create authentication server: %v", err)
	}
	pbauthn.RegisterSparkAuthnServiceServer(grpcServer, authnServer)

	healthService := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthService)
	healthService.SetServingStatus("spark-operator", grpc_health_v1.HealthCheckResponse_SERVING)

	log.Printf("Serving on port %d\n", args.Port)

	go runDKGOnStartup(dbClient, config)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}

func runDKGOnStartup(dbClient *ent.Client, config *so.Config) {
	time.Sleep(5 * time.Second)

	ctx := context.Background()
	tx, err := dbClient.Tx(ctx)
	if err != nil {
		log.Printf("Failed to create db transaction: %v", err)
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
		} else if err != nil {
			tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}()

	err = ent.RunDKGIfNeeded(tx, config)
	if err != nil {
		log.Printf("Failed to run DKG: %v", err)
	}
}
