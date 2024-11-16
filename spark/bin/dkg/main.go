package main

import (
	"errors"
	"flag"
	"log"

	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/dkg"
)

type Args struct {
	Index              uint64
	IdentityPrivateKey string
	OperatorsFilePath  string
	Threshold          uint64
	SignerAddress      string
	Port               uint64
	DatabasePath       string
	KeyCount           uint64
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
	flag.Uint64Var(&args.KeyCount, "key-count", 0, "Key count value")
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

	if args.KeyCount == 0 {
		return nil, errors.New("key count is required")
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

	err = dkg.GenerateKeys(config, args.KeyCount)
	if err != nil {
		log.Fatalf("Failed to generate keys: %v", err)
	}
}
