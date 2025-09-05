package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log" //nolint:depguard
	"log/slog"
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
	grpcmiddleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/lib/pq"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authninternal"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/chain"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	_ "github.com/lightsparkdev/spark/so/ent/runtime"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"

	sparkgrpc "github.com/lightsparkdev/spark/so/grpc"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/middleware"
	events "github.com/lightsparkdev/spark/so/stream"
	"github.com/lightsparkdev/spark/so/task"
	_ "github.com/mattn/go-sqlite3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
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
	DisableDKG                 bool
	DisableChainwatcher        bool
	SupportedNetworks          string
	AWS                        bool
	ServerCertPath             string
	ServerKeyPath              string
	RunDirectory               string
	RateLimiterEnabled         bool
	RateLimiterMemcachedAddrs  string
	RateLimiterWindow          time.Duration
	RateLimiterMaxRequests     int
	RateLimiterMethods         string
	EntDebug                   bool
}

func (a *args) SupportedNetworksList() []common.Network {
	var networks []common.Network
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
	flag.DurationVar(&args.ChallengeTimeout, "challenge-timeout", time.Minute, "Challenge timeout")
	flag.DurationVar(&args.SessionDuration, "session-duration", time.Minute*15, "Session duration")
	flag.BoolVar(&args.AuthzEnforced, "authz-enforced", true, "Enforce authorization checks")
	flag.BoolVar(&args.DisableDKG, "disable-dkg", false, "Disable DKG")
	flag.BoolVar(&args.DisableChainwatcher, "disable-chainwatcher", false, "Disable Chainwatcher")
	flag.StringVar(&args.SupportedNetworks, "supported-networks", "", "Supported networks")
	flag.BoolVar(&args.AWS, "aws", false, "Use AWS RDS")
	flag.StringVar(&args.ServerCertPath, "server-cert", "", "Path to server certificate")
	flag.StringVar(&args.ServerKeyPath, "server-key", "", "Path to server key")
	flag.StringVar(&args.RunDirectory, "run-dir", "", "Run directory for resolving relative paths")
	flag.BoolVar(&args.RateLimiterEnabled, "rate-limiter-enabled", false, "Enable rate limiting")
	flag.StringVar(&args.RateLimiterMemcachedAddrs, "rate-limiter-memcached-addrs", "", "Comma-separated list of Memcached addresses")
	flag.DurationVar(&args.RateLimiterWindow, "rate-limiter-window", 60*time.Second, "Rate limiter time window")
	flag.IntVar(&args.RateLimiterMaxRequests, "rate-limiter-max-requests", 100, "Maximum requests allowed in the time window")
	flag.StringVar(&args.RateLimiterMethods, "rate-limiter-methods", "", "Comma-separated list of methods to rate limit")
	flag.BoolVar(&args.EntDebug, "ent-debug", false, "Log all the SQL queries")

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

	return args, nil
}

func createRateLimiter(config *so.Config, opts ...middleware.RateLimiterOption) (*middleware.RateLimiter, error) {
	if !config.RateLimiter.Enabled {
		return nil, nil
	}

	return middleware.NewRateLimiter(config, opts...)
}

type BufferedBody struct {
	BodyReader io.ReadCloser
	Body       []byte
	Position   int
}

func (body *BufferedBody) Read(p []byte) (n int, err error) {
	err = nil
	if body.Body == nil {
		body.Body, err = io.ReadAll(body.BodyReader)
	}

	n = copy(p, body.Body[body.Position:])
	body.Position += n
	if err == nil && body.Position == len(body.Body) {
		err = io.EOF
	}

	return n, err
}

func (body *BufferedBody) Close() error {
	return body.BodyReader.Close()
}

func NewBufferedBody(bodyReader io.ReadCloser) *BufferedBody {
	return &BufferedBody{bodyReader, nil, 0}
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
		args.AWS,
		args.AuthzEnforced,
		args.SupportedNetworksList(),
		args.ServerCertPath,
		args.ServerKeyPath,
		args.RunDirectory,
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

	var valuesProvider knobs.KnobsValuesProvider
	if config.Knobs.IsEnabled() {
		if valuesProvider, err = knobs.NewKnobsK8ValuesProvider(errCtx); err != nil {
			// Knobs has failed to fetch the config, so the controllers will rely on the default values.
			slog.Error("Failed to create K8 knobs", "error", err)
		}
	}

	// Knobs service is always defined, no need to check for nil.
	// If the provider is nil, the knobs service will use the default values.
	knobsService := knobs.New(valuesProvider)

	dbDriver := config.DatabaseDriver()
	connector, err := so.NewDBConnector(errCtx, config, knobsService)
	if err != nil {
		log.Fatalf("Failed to create db connector: %v", err)
	}
	defer connector.Close()

	logger := slog.Default().With("component", "dbevents")
	dbEvents, err := db.NewDBEvents(errCtx, connector, logger)
	if err != nil {
		log.Fatalf("Failed to create db events: %v", err)
	}

	if config.Database.DBEventsEnabled != nil && *config.Database.DBEventsEnabled {
		errGrp.Go(func() error {
			return dbEvents.Start()
		})
	}

	for _, op := range config.SigningOperatorMap {
		op.SetTimeoutProvider(knobs.NewKnobsTimeoutProvider(knobsService, config.GRPC.ClientTimeout))
	}

	config.FrostGRPCConnectionFactory.SetTimeoutProvider(
		knobs.NewKnobsTimeoutProvider(knobsService, config.GRPC.ClientTimeout))

	var sqlDb entsql.ExecQuerier
	if dbDriver == "postgres" {
		sqlDb = stdlib.OpenDBFromPool(connector.Pool())
	} else {
		sqlDb = otelsql.OpenDB(connector, otelsql.WithSpanOptions(so.OtelSQLSpanOptions))
	}

	dialectDriver := entsql.NewDriver(dbDriver, entsql.Conn{ExecQuerier: sqlDb})

	var dbClient *ent.Client
	if args.EntDebug {
		dbClient = ent.NewClient(ent.Driver(dialectDriver), ent.Debug())
	} else {
		dbClient = ent.NewClient(ent.Driver(dialectDriver))
	}

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

	frostConnection, err := config.NewFrostGRPCConnection()
	if err != nil {
		log.Fatalf("Failed to create frost client: %v", err)
	}

	if !args.DisableChainwatcher {
		// Chain watchers
		for network, bitcoindConfig := range config.BitcoindConfigs {
			network := network
			bitcoindConfig := bitcoindConfig
			errGrp.Go(func() error {
				chainCtx, chainCancel := context.WithCancel(errCtx)
				defer chainCancel()

				logger := slog.Default().With("component", "chainwatcher", "network", network)
				chainCtx = logging.Inject(chainCtx, logger)

				err := chain.WatchChain(
					chainCtx,
					config,
					dbClient,
					bitcoindConfig,
				)
				if err != nil {
					logger.Error("Error in chain watcher", "error", err)
					return err
				}

				if errCtx.Err() == nil {
					// This technically isn't an error, but raise it as one because our chain watcher should never
					// stop unless we explicitly tell it to when shutting down!
					return fmt.Errorf("chain watcher for %s stopped unexpectedly", network)
				}

				return nil
			})
		}
	}

	if !args.DisableDKG {
		// Scheduled tasks setup
		cronCtx, cronCancel := context.WithCancel(errCtx)
		defer cronCancel()

		taskLogger := slog.Default().With("component", "cron")
		cronCtx = logging.Inject(cronCtx, taskLogger)

		taskLogger.Info("Starting scheduler")
		taskMonitor, err := task.NewMonitor()
		if err != nil {
			log.Fatalf("Failed to create task monitor: %v", err)
		}
		scheduler, err := gocron.NewScheduler(
			gocron.WithGlobalJobOptions(
				gocron.WithContext(cronCtx),
				gocron.WithSingletonMode(gocron.LimitModeReschedule),
			),
			gocron.WithLogger(taskLogger),
			gocron.WithMonitorStatus(taskMonitor),
		)
		if err != nil {
			log.Fatalf("Failed to create scheduler: %v", err)
		}
		for _, scheduled := range task.AllScheduledTasks() {
			// Don't run the task if the task specifies it should not be run in
			// test environments and RunningLocally is set (eg. we are in a test environment)
			if (!args.RunningLocally || scheduled.RunInTestEnv) && !scheduled.Disabled {
				err := scheduled.Schedule(scheduler, config, dbClient, knobsService)
				if err != nil {
					log.Fatalf("Failed to create job: %v", err)
				}
			}
		}
		scheduler.Start()
		defer scheduler.Shutdown() //nolint:errcheck
	}

	errGrp.Go(func() error {
		return task.RunStartupTasks(config, dbClient, args.RunningLocally, knobsService)
	})

	sessionTokenCreatorVerifier, err := authninternal.NewSessionTokenCreatorVerifier(config.IdentityPrivateKey, nil)
	if err != nil {
		log.Fatalf("Failed to create token verifier: %v", err)
	}

	var rateLimiter *middleware.RateLimiter
	slog.Info("Rate limiter config", "enabled", config.RateLimiter.Enabled, "window", config.RateLimiter.Window, "max_requests", config.RateLimiter.MaxRequests, "methods", config.RateLimiter.Methods)
	if config.RateLimiter.Enabled {
		var err error
		rateLimiter, err = createRateLimiter(config, middleware.WithKnobs(knobsService))
		if err != nil {
			log.Fatalf("Failed to create rate limiter: %v", err)
		}
	}

	clientInfoProvider := sparkgrpc.NewGRPCClientInfoProvider(config.XffClientIpPosition)
	var tableLogger *logging.TableLogger
	if args.LogRequestStats && args.LogJSON {
		tableLogger = logging.NewTableLogger(clientInfoProvider)
	}

	serverOpts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	}

	// Establish base values from config, then allow runtime knobs to override
	// grpcConnTimeout, grpcKeepaliveTime and grpcKeepaliveTimeout are set when
	// the server is created and cannot be changed at runtime.
	grpcConnTimeout := knobs.GetDurationSeconds(knobsService, knobs.KnobGrpcServerConnectionTimeout, config.GRPC.ServerConnectionTimeout)
	grpcKeepaliveTime := knobs.GetDurationSeconds(knobsService, knobs.KnobGrpcServerKeepaliveTime, config.GRPC.ServerKeepaliveTime)
	grpcKeepaliveTimeout := knobs.GetDurationSeconds(knobsService, knobs.KnobGrpcServerKeepaliveTimeout, config.GRPC.ServerKeepaliveTimeout)

	// This uses SetDeadline in net.Conn to set the timeout for the connection
	// establishment, after which the connection is closed with error
	// `DeadlineExceeded`.
	if grpcConnTimeout > 0 {
		serverOpts = append(serverOpts, grpc.ConnectionTimeout(grpcConnTimeout))
	}

	// Keepalive detects dead connections and closes them.
	// Time is the interval between keepalive pings.
	// Timeout is the interval between keepalive pings after which the connection is closed.
	serverOpts = append(serverOpts, grpc.KeepaliveParams(keepalive.ServerParameters{
		Time:    grpcKeepaliveTime,
		Timeout: grpcKeepaliveTimeout,
	}))

	var concurrencyGuard sparkgrpc.ResourceLimiter
	if config.GRPC.ServerConcurrencyLimitEnabled {
		slog.Info("Concurrency limit enabled", "limit", config.GRPC.ServerConcurrencyLimit)
		concurrencyGuard = sparkgrpc.NewConcurrencyGuard(knobsService, config.GRPC.ServerConcurrencyLimit)
	} else {
		slog.Info("Concurrency limit disabled")
		concurrencyGuard = &sparkgrpc.NoopResourceLimiter{}
	}

	var eventsRouter *events.EventRouter
	if config.Database.DBEventsEnabled != nil && *config.Database.DBEventsEnabled {
		eventsLogger := slog.Default().With("component", "events_router")
		eventsRouter = events.NewEventRouter(dbClient, dbEvents, eventsLogger)
	}

	// Add Interceptors aka gRPC middleware
	//
	// Interceptors wrap RPC handlers so we can apply cross‑cutting concerns in one place
	// and in a defined order. We install separate chains for unary (request/response)
	// and streaming RPCs.
	serverOpts = append(serverOpts,
		grpc.UnaryInterceptor(grpcmiddleware.ChainUnaryServer(
			sparkerrors.ErrorMaskingInterceptor(config.ReturnDetailedErrors),
			sparkerrors.ErrorWrappingInterceptor(),
			sparkgrpc.LogInterceptor(tableLogger),
			// Inject knobs into context for unary requests
			func() grpc.UnaryServerInterceptor {
				return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
					ctx = knobs.InjectKnobsService(ctx, knobsService)
					return handler(ctx, req)
				}
			}(),
			sparkgrpc.ConcurrencyInterceptor(concurrencyGuard),
			sparkgrpc.TimeoutInterceptor(knobsService, config.GRPC.ServerUnaryHandlerTimeout),
			sparkgrpc.SparkTokenMetricsInterceptor(),
			sparkgrpc.PanicRecoveryInterceptor(config.ReturnDetailedPanicErrors),
			func() grpc.UnaryServerInterceptor {
				if rateLimiter != nil {
					return rateLimiter.UnaryServerInterceptor()
				}
				return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
					return handler(ctx, req)
				}
			}(),
			sparkgrpc.DatabaseSessionMiddleware(
				db.NewDefaultSessionFactory(dbClient),
				config.Database.NewTxTimeout,
			),
			helper.SigningCommitmentInterceptor(config.SigningOperatorMap, knobsService),
			authn.NewInterceptor(sessionTokenCreatorVerifier).AuthnInterceptor,
			authz.NewAuthzInterceptor(authz.NewAuthzConfig(
				authz.WithMode(config.ServiceAuthz.Mode),
				authz.WithAllowedIPs(config.ServiceAuthz.IPAllowlist),
				authz.WithProtectedServices(GetProtectedServices()),
				authz.WithXffClientIpPosition(config.XffClientIpPosition),
			)).UnaryServerInterceptor,
			sparkgrpc.ValidationInterceptor(),
		)),
		grpc.StreamInterceptor(grpcmiddleware.ChainStreamServer(
			sparkerrors.ErrorWrappingStreamingInterceptor(),
			sparkgrpc.StreamLogInterceptor(),
			sparkgrpc.PanicRecoveryStreamInterceptor(),
			authn.NewInterceptor(sessionTokenCreatorVerifier).StreamAuthnInterceptor,
			authz.NewAuthzInterceptor(authz.NewAuthzConfig(
				authz.WithMode(config.ServiceAuthz.Mode),
				authz.WithAllowedIPs(config.ServiceAuthz.IPAllowlist),
				authz.WithProtectedServices(GetProtectedServices()),
				authz.WithXffClientIpPosition(config.XffClientIpPosition),
			)).StreamServerInterceptor,
			sparkgrpc.StreamValidationInterceptor(),
		)),
	)

	cert, err := tls.LoadX509KeyPair(args.ServerCertPath, args.ServerKeyPath)
	if err != nil {
		log.Fatalf("Failed to load server certificate: %v", err)
	}

	tlsConfig := tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.NoClientCert,
		MinVersion:   tls.VersionTLS12,
	}

	creds := credentials.NewTLS(&tlsConfig)
	serverOpts = append(serverOpts, grpc.Creds(creds))
	grpcServer := grpc.NewServer(serverOpts...)

	var mockAction *common.MockAction
	if args.RunningLocally {
		mockAction = common.NewMockAction()
	}

	err = RegisterGrpcServers(
		grpcServer,
		args,
		config,
		dbClient,
		frostConnection,
		sessionTokenCreatorVerifier,
		mockAction,
		eventsRouter,
	)
	if err != nil {
		log.Fatalf("Failed to register all gRPC servers: %v", err)
	}

	// Web compatibility layer
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
	mux.Handle("/",
		otelhttp.NewHandler(
			http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					// The gRPC server doesn't read the request body until EOF before processing
					// the request. This can result in the HTTP server receiving a DATA(END_FRAME)
					// frame after sending the response, which elicits a RST_STREAM(STREAM_CLOSED)
					// frame. ALB and nginx then respond to the client with RST_STREAM(INTERNAL_ERROR)
					// which causes the request to fail. The workaround is to buffer the entire
					// request body before passing to the gRPC server.
					r.Body = NewBufferedBody(r.Body)

					if strings.ToLower(r.Header.Get("Content-Type")) == "application/grpc" {
						grpcServer.ServeHTTP(w, r)
						return
					}
					wrappedGrpc.ServeHTTP(w, r)
				},
			),
			"server",
			otelhttp.WithTracerProvider(noop.TracerProvider{}), // Disable tracing, let gRPC server handle it.
			otelhttp.WithMetricAttributesFn(func(r *http.Request) []attribute.KeyValue {
				return []attribute.KeyValue{
					// Technically we shouldn't be using the path here because of cardinality, but since we know
					// this is just routing to the gRPC server, we can assume the path is reasonable.
					attribute.String(string(semconv.HTTPRouteKey), r.URL.Path),
				}
			}),
		),
	)

	server := &http.Server{
		Addr:      fmt.Sprintf(":%d", args.Port),
		Handler:   mux,
		TLSConfig: &tlsConfig,
	}

	errGrp.Go(func() error {
		if err := server.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server failed", "error", err)
			return err
		}

		return nil
	})

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

	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownRelease()

	slog.Info("Stopping HTTP server...")
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server failed to shutdown gracefully", "error", err)
	} else {
		slog.Info("HTTP server stopped")
	}

	if err := errGrp.Wait(); err != nil {
		slog.Error("Shutdown due to error", "error", err)
	}
}
