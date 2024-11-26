package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/dkg"
	"github.com/lightsparkdev/spark-go/so/ent"
	sparkgrpc "github.com/lightsparkdev/spark-go/so/grpc"
	_ "github.com/mattn/go-sqlite3"
	"google.golang.org/grpc"
)

type Args struct {
	Index              uint64
	IdentityPrivateKey string
	OperatorsFilePath  string
	Threshold          uint64
	SignerAddress      string
	Port               uint64
	DatabasePath       string
}

func loadArgs() (*Args, error) {
	args := &Args{}

	// Define flags
	flag.Uint64Var(&args.Index, "index", 0, "Index value")
	flag.StringVar(&args.IdentityPrivateKey, "key", "", "Identity private key")
	flag.StringVar(&args.OperatorsFilePath, "operators", "", "Path to operators file")
	flag.Uint64Var(&args.Threshold, "threshold", 0, "Threshold value")
	flag.StringVar(&args.SignerAddress, "signer", "", "Signer address")
	flag.Uint64Var(&args.Port, "port", 0, "Port value")
	flag.StringVar(&args.DatabasePath, "database", "", "Path to database file")
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

	return args, nil
}

func main() {
	args, err := loadArgs()
	if err != nil {
		log.Fatalf("Failed to load args: %v", err)
	}

	config, err := so.NewConfig(args.Index, args.IdentityPrivateKey, args.OperatorsFilePath, args.Threshold, args.SignerAddress, args.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}

	dbDriver := config.DatabaseDriver()
	dbClient, err := ent.Open(dbDriver, config.DatabasePath+"?_fk=1")
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

	go runDKGOnStartup(dbClient, config)

	dkgServer := dkg.NewDkgServer(frostConnection, config)

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(common.DbSessionMiddleware(dbClient)))
	pb.RegisterDKGServiceServer(grpcServer, dkgServer)

	sparkInternalServer := sparkgrpc.NewSparkInternalServer(config)
	pb.RegisterSparkInternalServiceServer(grpcServer, sparkInternalServer)

	sparkServer := sparkgrpc.NewSparkServer(config)
	pb.RegisterSparkServiceServer(grpcServer, sparkServer)

	log.Printf("Serving on port %d\n", args.Port)
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

	err = dkg.RunDKGIfNeeded(tx, config)
	if err != nil {
		log.Fatalf("Failed to run DKG: %v", err)
	}
}
