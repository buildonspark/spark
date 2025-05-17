package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log" //nolint:depguard
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"golang.org/x/sync/errgroup"

	"github.com/XSAM/otelsql"
	"github.com/go-co-op/gocron/v2"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/lib/pq"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbauthn "github.com/lightsparkdev/spark/proto/spark_authn"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbtree "github.com/lightsparkdev/spark/proto/spark_tree"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authninternal"
	"github.com/lightsparkdev/spark/so/chain"
	"github.com/lightsparkdev/spark/so/dkg"
	"github.com/lightsparkdev/spark/so/ent"
	_ "github.com/lightsparkdev/spark/so/ent/runtime"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	sparkgrpc "github.com/lightsparkdev/spark/so/grpc"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/lrc20"
	"github.com/lightsparkdev/spark/so/middleware"
	"github.com/lightsparkdev/spark/so/task"
	_ "github.com/mattn/go-sqlite3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

type args struct {
	LogLevel                   string
	LogJSON                    bool
	LogRequestStats            bool
	ConfigFilePath             string
	Index                      uint64
	IdentityPrivateKeyFilePath string
	OperatorsFilePath          string
	Threshold                  uint64
	SignerAddress              string
	Port                       uint64
	DatabasePath               string
	RunningLocally             bool
	ChallengeTimeout           time.Duration
	SessionDuration            time.Duration
	AuthzEnforced              bool
	DKGCoordinatorAddress      string
	DisableDKG                 bool
	SupportedNetworks          string
	AWS                        bool
	ServerCertPath             string
	ServerKeyPath              string
	DKGLimitOverride           uint64
	RunDirectory               string
	ReturnDetailedPanicErrors  bool
	RateLimiterEnabled         bool
	RateLimiterMemcachedAddrs  string
	RateLimiterWindow          time.Duration
	RateLimiterMaxRequests     int
	RateLimiterMethods         string
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
	flag.BoolVar(&args.LogRequestStats, "log-request-stats", false, "Log request stats (requires log-json)")
	flag.StringVar(&args.ConfigFilePath, "config", "so_config.yaml", "Path to config file")
	flag.Uint64Var(&args.Index, "index", 0, "Index value")
	flag.StringVar(&args.IdentityPrivateKeyFilePath, "key", "", "Identity private key")
	flag.StringVar(&args.OperatorsFilePath, "operators", "", "Path to operators file")
	flag.Uint64Var(&args.Threshold, "threshold", 0, "Threshold value")
	flag.StringVar(&args.SignerAddress, "signer", "", "Signer address")
	flag.Uint64Var(&args.Port, "port", 0, "Port value")
	flag.StringVar(&args.DatabasePath, "database", "", "Path to database file")
	flag.BoolVar(&args.RunningLocally, "local", false, "Running locally")
	flag.DurationVar(&args.ChallengeTimeout, "challenge-timeout", time.Duration(time.Minute), "Challenge timeout")
	flag.DurationVar(&args.SessionDuration, "session-duration", time.Duration(time.Minute*15), "Session duration")
	flag.BoolVar(&args.AuthzEnforced, "authz-enforced", true, "Enforce authorization checks")
	flag.StringVar(&args.DKGCoordinatorAddress, "dkg-address", "", "DKG coordinator address")
	flag.BoolVar(&args.DisableDKG, "disable-dkg", false, "Disable DKG")
	flag.StringVar(&args.SupportedNetworks, "supported-networks", "", "Supported networks")
	flag.BoolVar(&args.AWS, "aws", false, "Use AWS RDS")
	flag.StringVar(&args.ServerCertPath, "server-cert", "", "Path to server certificate")
	flag.StringVar(&args.ServerKeyPath, "server-key", "", "Path to server key")
	flag.Uint64Var(&args.DKGLimitOverride, "dkg-limit-override", 0, "Override the DKG limit")
	flag.StringVar(&args.RunDirectory, "run-dir", "", "Run directory for resolving relative paths")
	// TODO(CNT-154): Consider setting to false by default before productionization.
	flag.BoolVar(&args.ReturnDetailedPanicErrors, "return-detailed-panic-errors", true, "Return detailed panic errors to client")
	flag.BoolVar(&args.RateLimiterEnabled, "rate-limiter-enabled", false, "Enable rate limiting")
	flag.StringVar(&args.RateLimiterMemcachedAddrs, "rate-limiter-memcached-addrs", "", "Comma-separated list of Memcached addresses")
	flag.DurationVar(&args.RateLimiterWindow, "rate-limiter-window", 60*time.Second, "Rate limiter time window")
	flag.IntVar(&args.RateLimiterMaxRequests, "rate-limiter-max-requests", 100, "Maximum requests allowed in the time window")
	flag.StringVar(&args.RateLimiterMethods, "rate-limiter-methods", "", "Comma-separated list of methods to rate limit")

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
		return nil, errors.New("invalid log level")
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

	return args, nil
}

func createRateLimiter(config *so.Config) (*middleware.RateLimiter, error) {
	if !config.RateLimiter.Enabled {
		return nil, nil
	}

	return middleware.NewRateLimiter(config)
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
		args.DKGLimitOverride,
		args.RunDirectory,
		args.ReturnDetailedPanicErrors,
		so.RateLimiterConfig{
			Enabled:     args.RateLimiterEnabled,
			Window:      args.RateLimiterWindow,
			MaxRequests: args.RateLimiterMaxRequests,
			Methods:     strings.Split(args.RateLimiterMethods, ","),
		},
	)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}

	sigCtx, done := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer done()

	errGrp, errCtx := errgroup.WithContext(sigCtx)

	// OBSERVABILITY
	promExporter, err := otelprom.New()
	if err != nil {
		log.Fatalf("Failed to create prometheus exporter: %v", err)
	}
	meterProvider := metric.NewMeterProvider(metric.WithReader(promExporter))
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if config.Tracing.Enabled {
		shutdown, err := common.ConfigureTracing(errCtx, config.Tracing)
		if err != nil {
			log.Fatalf("Failed to configure tracing: %v", err)
		}
		defer func() {
			shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownRelease()

			slog.Info("Shutting down tracer provider")
			if err := shutdown(shutdownCtx); err != nil {
				slog.Error("Error shutting down tracer provider", "error", err)
			} else {
				slog.Info("Tracer provider shut down")
			}
		}()
	}

	dbDriver := config.DatabaseDriver()
	connector, err := so.NewDBConnector(errCtx, config.DatabasePath, config.AWS)
	if err != nil {
		log.Fatalf("Failed to create db connector: %v", err)
	}
	defer connector.Close()

	var db entsql.ExecQuerier
	if dbDriver == "postgres" {
		db = stdlib.OpenDBFromPool(connector.Pool())
	} else {
		db = otelsql.OpenDB(connector, otelsql.WithSpanOptions(so.OtelSQLSpanOptions))
	}

	dialectDriver := entsql.NewDriver(dbDriver, entsql.Conn{ExecQuerier: db})
	dbClient := ent.NewClient(ent.Driver(dialectDriver))
	dbClient.Intercept(ent.DatabaseStatsInterceptor(10 * time.Second))
	defer dbClient.Close()

	if dbDriver == "sqlite3" {
		sqliteDb, _ := sql.Open("sqlite3", config.DatabasePath)
		if _, err := sqliteDb.ExecContext(errCtx, "PRAGMA journal_mode=WAL;"); err != nil {
			log.Fatalf("Failed to set journal_mode: %v", err)
		}
		if _, err := sqliteDb.ExecContext(errCtx, "PRAGMA busy_timeout=5000;"); err != nil {
			log.Fatalf("Failed to set busy_timeout: %v", err)
		}
		sqliteDb.Close()
	}

	frostConnection, err := common.NewGRPCConnectionWithoutTLS(args.SignerAddress, nil)
	if err != nil {
		log.Fatalf("Failed to create frost client: %v", err)
	}

	lrc20Client, err := lrc20.NewClient(
		config,
		slog.Default().With("component", "lrc20_client"),
	)
	if err != nil {
		log.Fatalf("Failed to create LRC20 client: %v", err)
	}
	defer lrc20Client.Close() //nolint:errcheck

	for network, bitcoindConfig := range config.BitcoindConfigs {
		errGrp.Go(func() error {
			chainCtx, chainCancel := context.WithCancel(errCtx)
			defer chainCancel()

			logger := slog.Default().With("component", "chainwatcher", "network", network)
			chainCtx = logging.Inject(chainCtx, logger)

			err := chain.WatchChain(
				chainCtx,
				dbClient,
				lrc20Client,
				bitcoindConfig,
			)
			if err != nil {
				logger.Error("Error in chain watcher", "error", err)
				return err
			}

			if errCtx.Err() == nil {
				// This technically isn't an error, but raise it as one because our chain watcher should never
				// stop unless we explicitly tell it to when shutting down!
				return fmt.Errorf("chain watcher stopped unexpectedly")
			}

			return nil
		})
	}

	if !args.RunningLocally {
		cronCtx, cronCancel := context.WithCancel(errCtx)
		defer cronCancel()

		logger := slog.Default().With("component", "cron")
		cronCtx = logging.Inject(cronCtx, logger)

		logger.Info("Starting scheduler")
		scheduler, err := gocron.NewScheduler(
			gocron.WithGlobalJobOptions(gocron.WithContext(cronCtx)),
			gocron.WithLogger(logger),
		)
		if err != nil {
			log.Fatalf("Failed to create scheduler: %v", err)
		}
		for _, task := range task.AllTasks() {

			err := task.Schedule(scheduler, config, dbClient)
			if err != nil {
				log.Fatalf("Failed to create job: %v", err)
			}
		}
		scheduler.Start()
		defer scheduler.Shutdown() //nolint:errcheck
	}

	sessionTokenCreatorVerifier, err := authninternal.NewSessionTokenCreatorVerifier(config.IdentityPrivateKey, nil)
	if err != nil {
		log.Fatalf("Failed to create token verifier: %v", err)
	}

	var rateLimiter *middleware.RateLimiter
	if config.RateLimiter.Enabled {
		var err error
		rateLimiter, err = createRateLimiter(config)
		if err != nil {
			log.Fatalf("Failed to create rate limiter: %v", err)
		}
	}

	serverOpts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
			sparkerrors.ErrorInterceptor(),
			helper.LogInterceptor(args.LogJSON && args.LogRequestStats),
			sparkgrpc.PanicRecoveryInterceptor(config.ReturnDetailedPanicErrors),
			ent.DbSessionMiddleware(dbClient),
			authn.NewAuthnInterceptor(sessionTokenCreatorVerifier).AuthnInterceptor,
			sparkgrpc.ValidationInterceptor(),
			func() grpc.UnaryServerInterceptor {
				if rateLimiter != nil {
					return rateLimiter.UnaryServerInterceptor()
				}
				return func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
					return handler(ctx, req)
				}
			}(),
		)),
		grpc.StreamInterceptor(grpc_middleware.ChainStreamServer(
			authn.NewAuthnInterceptor(sessionTokenCreatorVerifier).StreamAuthnInterceptor,
			sparkgrpc.StreamValidationInterceptor(),
		)),
	}

	var grpcServer *grpc.Server
	var tlsConfig *tls.Config
	if args.ServerCertPath != "" && args.ServerKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(args.ServerCertPath, args.ServerKeyPath)
		if err != nil {
			log.Fatalf("Failed to load server certificate: %v", err)
		}
		creds := credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.NoClientCert,
			MinVersion:   tls.VersionTLS12,
		})
		serverOpts = append(serverOpts, grpc.Creds(creds))
		grpcServer = grpc.NewServer(serverOpts...)
		slog.Info(fmt.Sprintf("Server starting with TLS on: %v", args.ServerCertPath))
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	} else {
		grpcServer = grpc.NewServer(serverOpts...)
		tlsConfig = nil
	}

	if !args.DisableDKG {
		dkgServer := dkg.NewServer(frostConnection, config)
		pbdkg.RegisterDKGServiceServer(grpcServer, dkgServer)
	}

	sparkInternalServer := sparkgrpc.NewSparkInternalServer(config, lrc20Client)

	pbinternal.RegisterSparkInternalServiceServer(grpcServer, sparkInternalServer)

	sparkServer := sparkgrpc.NewSparkServer(config, dbClient, lrc20Client)
	pbspark.RegisterSparkServiceServer(grpcServer, sparkServer)

	treeServer := sparkgrpc.NewSparkTreeServer(config, dbClient)
	pbtree.RegisterSparkTreeServiceServer(grpcServer, treeServer)

	if args.RunningLocally {
		mockServer := sparkgrpc.NewMockServer(config)
		pbmock.RegisterMockServiceServer(grpcServer, mockServer)
		go runDKGOnStartup(errCtx, dbClient, config)
	}

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

	wrappedGrpc := grpcweb.WrapServer(grpcServer,
		grpcweb.WithOriginFunc(func(_ string) bool {
			return true
		}),
		grpcweb.WithCorsForRegisteredEndpointsOnly(false),
	)

	mux := http.NewServeMux()
	mux.Handle("/-/ready", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.ToLower(r.Header.Get("Content-Type")) == "application/grpc" {
			grpcServer.ServeHTTP(w, r)
			return
		}
		wrappedGrpc.ServeHTTP(w, r)
	}))

	var server *http.Server
	if tlsConfig != nil {
		server = &http.Server{
			Addr:      fmt.Sprintf(":%d", args.Port),
			Handler:   mux,
			TLSConfig: tlsConfig,
		}

		errGrp.Go(func() error {
			slog.Info(fmt.Sprintf("Serving on port %d (TLS)", args.Port))
			if err := server.ListenAndServeTLS(args.ServerCertPath, args.ServerKeyPath); !errors.Is(err, http.ErrServerClosed) {
				slog.Error("Failed to serve", "error", err)
				return err
			}

			if errCtx.Err() == nil {
				// This technically isn't an error, but raise it as one because our gRPC server should never
				// stop unless we explicitly tell it to when shutting down!
				return fmt.Errorf("gRPC server stopped unexpectedly")
			}

			return nil
		})
	} else {
		errGrp.Go(func() error {
			lis, err := net.Listen("tcp", fmt.Sprintf(":%d", args.Port))
			if err != nil {
				slog.Error("Failed to listen to TCP socket", "error", err)
				return err
			}

			slog.Info(fmt.Sprintf("Serving on port %d (non-TLS)", args.Port))
			if err := grpcServer.Serve(lis); !errors.Is(err, grpc.ErrServerStopped) {
				slog.Error("Failed to serve", "error", err)
				return err
			}

			if errCtx.Err() == nil {
				// This technically isn't an error, but raise it as one because our gRPC server should never
				// stop unless we explicitly tell it to when shutting down!
				return fmt.Errorf("gRPC server stopped unexpectedly")
			}

			return nil
		})
	}

	// Now we wait... for something to fail.
	<-errCtx.Done()

	if sigCtx.Err() != nil {
		slog.Info("Received shutdown signal, shutting down gracefully...")
	} else {
		slog.Error("Shutting down due to error...")
	}

	slog.Info("Stopping gRPC server...")
	grpcServer.GracefulStop()
	slog.Info("gRPC server stopped")
	if server != nil {
		shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownRelease()

		slog.Info("Stopping HTTP server...")
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP server failed to shutdown gracefully", "error", err)
		} else {
			slog.Info("HTTP server stopped")
		}
	}

	if err := errGrp.Wait(); err != nil {
		slog.Error("Shutdown due to error", "error", err)
	}
}

func runDKGOnStartup(ctx context.Context, dbClient *ent.Client, config *so.Config) {
	time.Sleep(5 * time.Second)
	err := ent.RunDKGIfNeeded(ctx, dbClient, config)
	if err != nil {
		slog.Error("Failed to run DKG", "error", err)
	}
}
