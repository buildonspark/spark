package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/go-co-op/gocron/v2"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	_ "github.com/lib/pq"
	"github.com/lightsparkdev/spark-go/common"
	pbdkg "github.com/lightsparkdev/spark-go/proto/dkg"
	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
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
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/task"
	_ "github.com/mattn/go-sqlite3"
	"google.golang.org/grpc"
)

type args struct {
	Index                 uint64
	IdentityPrivateKey    string
	OperatorsFilePath     string
	Threshold             uint64
	SignerAddress         string
	Port                  uint64
	DatabasePath          string
	MockOnchain           bool
	ChallengeTimeout      time.Duration
	SessionDuration       time.Duration
	AuthzEnforced         bool
	DKGCoordinatorAddress string
	DisableDKG            bool
}

func loadArgs() (*args, error) {
	args := &args{}

	// Define flags
	flag.Uint64Var(&args.Index, "index", 0, "Index value")
	flag.StringVar(&args.IdentityPrivateKey, "key", "", "Identity private key")
	flag.StringVar(&args.OperatorsFilePath, "operators", "", "Path to operators file")
	flag.Uint64Var(&args.Threshold, "threshold", 0, "Threshold value")
	flag.StringVar(&args.SignerAddress, "signer", "", "Signer address")
	flag.Uint64Var(&args.Port, "port", 0, "Port value")
	flag.StringVar(&args.DatabasePath, "database", "", "Path to database file")
	flag.BoolVar(&args.MockOnchain, "mock-onchain", false, "Mock onchain tx")
	flag.DurationVar(&args.ChallengeTimeout, "challenge-timeout", time.Duration(time.Minute), "Challenge timeout")
	flag.DurationVar(&args.SessionDuration, "session-duration", time.Duration(time.Minute*15), "Session duration")
	flag.BoolVar(&args.AuthzEnforced, "authz-enforced", true, "Enforce authorization checks")
	flag.StringVar(&args.DKGCoordinatorAddress, "dkg-address", "", "DKG coordinator address")
	flag.BoolVar(&args.DisableDKG, "disable-dkg", false, "Disable DKG")
	// Parse flags
	flag.Parse()

	if args.IdentityPrivateKey == "" || len(args.IdentityPrivateKey) != 64 {
		return nil, errors.New("identity private key is required and must be 32 bytes hex string")
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

	if args.DKGCoordinatorAddress == "" {
		args.DKGCoordinatorAddress = "localhost:" + strconv.Itoa(int(args.Port))
	}

	return args, nil
}

func main() {
	log.SetFlags(log.Lshortfile | log.Llongfile | log.Ldate | log.Ltime)

	args, err := loadArgs()
	if err != nil {
		log.Fatalf("Failed to load args: %v", err)
	}

	config, err := so.NewConfig(
		args.Index,
		args.IdentityPrivateKey,
		args.OperatorsFilePath,
		args.Threshold,
		args.SignerAddress,
		args.DatabasePath,
		args.AuthzEnforced,
		args.DKGCoordinatorAddress,
	)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}

	dbDriver := config.DatabaseDriver()
	dbClient, err := ent.Open(dbDriver, config.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to create database client: %v", err)
	}
	defer dbClient.Close()

	if dbDriver == "sqlite3" {
		sqliteDb, _ := sql.Open("sqlite3", config.DatabasePath)
		if _, err := sqliteDb.ExecContext(context.Background(), "PRAGMA journal_mode=WAL;"); err != nil {
			log.Fatalf("Failed to set journal_mode: %v", err)
		}
		if _, err := sqliteDb.ExecContext(context.Background(), "PRAGMA busy_timeout=5000;"); err != nil {
			log.Fatalf("Failed to set busy_timeout: %v", err)
		}
		sqliteDb.Close()
	}

	if err := dbClient.Schema.Create(context.Background()); err != nil {
		log.Fatalf("failed creating schema resources: %v", err)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", args.Port))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", args.Port, err)
	}

	frostConnection, err := common.NewGRPCConnection(args.SignerAddress)
	if err != nil {
		log.Fatalf("Failed to create frost client: %v", err)
	}

	s, err := gocron.NewScheduler()
	if err != nil {
		log.Fatalf("Failed to create scheduler: %v", err)
	}

	go func() {
		err := chain.WatchChain(dbClient, config.Network)
		if err != nil {
			log.Fatalf("Failed to watch chain: %v", err)
		}
	}()

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

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
		ent.DbSessionMiddleware(dbClient),
		authn.NewAuthnInterceptor(sessionTokenCreatorVerifier).AuthnInterceptor,
	)))

	if !args.DisableDKG {
		dkgServer := dkg.NewServer(frostConnection, config)
		pbdkg.RegisterDKGServiceServer(grpcServer, dkgServer)
	}

	var onchainHelper helper.OnChainHelper = &helper.DemoOnChainHelper{}
	if args.MockOnchain {
		onchainHelper = helper.NewMockOnChainHelper()
		mockServer := sparkgrpc.NewMockServer(config, onchainHelper.(*helper.MockOnChainHelper))
		pbmock.RegisterMockServiceServer(grpcServer, mockServer)
	}
	sparkInternalServer := sparkgrpc.NewSparkInternalServer(config, onchainHelper)
	pbinternal.RegisterSparkInternalServiceServer(grpcServer, sparkInternalServer)

	sparkServer := sparkgrpc.NewSparkServer(config, onchainHelper)
	pbspark.RegisterSparkServiceServer(grpcServer, sparkServer)

	treeServer := sparkgrpc.NewSparkTreeServer(config)
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
		log.Fatalf("Failed to create db transaction: %v", err)
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		} else if err != nil {
			tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}()

	err = ent.RunDKGIfNeeded(tx, config)
	if err != nil {
		log.Fatalf("Failed to run DKG: %v", err)
	}
}
