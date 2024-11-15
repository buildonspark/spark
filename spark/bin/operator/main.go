package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"

	"github.com/lightsparkdev/spark-go/frost"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/dkg"
	"github.com/lightsparkdev/spark-go/so/ent"
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

	dbClient, err := ent.Open(config.DatabaseDriver(), config.DatabasePath+"?_fk=1")
	if err != nil {
		log.Fatalf("Failed to create database client: %v", err)
	}
	defer dbClient.Close()

	if err := dbClient.Schema.Create(context.Background()); err != nil {
		log.Fatalf("failed creating schema resources: %v", err)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", args.Port))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", args.Port, err)
	}

	frostClient, err := frost.NewFrostClient(args.SignerAddress)
	if err != nil {
		log.Fatalf("Failed to create frost client: %v", err)
	}

	dkgServer := dkg.NewDkgServer(*frostClient, config)

	grpcServer := grpc.NewServer()
	pb.RegisterDKGServiceServer(grpcServer, dkgServer)
	log.Printf("Serving on port %d\n", args.Port)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
