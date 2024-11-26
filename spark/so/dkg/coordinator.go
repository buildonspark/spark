package dkg

import (
	"context"
	"log"
	"sync"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
)

func RunDKGIfNeeded(db *ent.Tx, config *so.Config) error {
	count, err := db.SigningKeyshare.Query().Where(
		signingkeyshare.StatusEQ(schema.KeyshareStatusAvailable),
		signingkeyshare.CoordinatorIndexEQ(config.Index),
	).Count(context.Background())
	if err != nil {
		return err
	}
	if uint64(count) >= spark.DKGThreshold {
		return nil
	}

	log.Printf("DKG started, only %d keyshares available", count)
	return GenerateKeys(config, spark.DKGKeyCount)
}

func GenerateKeys(config *so.Config, keyCount uint64) error {
	log.Printf("Generating %d keys", keyCount)
	// Init clients
	clientMap := make(map[string]pb.DKGServiceClient)
	for identifier, operator := range config.SigningOperatorMap {
		connection, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return err
		}
		defer connection.Close()
		client := pb.NewDKGServiceClient(connection)
		clientMap[identifier] = client
	}

	// Initiate DKG
	requestId, err := uuid.NewV7()
	if err != nil {
		return err
	}
	requestIdString := requestId.String()
	initRequest := &pb.InitiateDkgRequest{
		RequestId:        requestIdString,
		KeyCount:         keyCount,
		MinSigners:       config.Threshold,
		MaxSigners:       uint64(len(config.SigningOperatorMap)),
		CoordinatorIndex: config.Index,
	}

	round1Packages := make([]*pb.PackageMap, int(keyCount))

	for identifier, client := range clientMap {
		log.Printf("Initiating DKG with %s", identifier)
		round1Response, err := client.InitiateDkg(context.Background(), initRequest)
		if err != nil {
			return err
		}
		for i, p := range round1Response.Round1Package {
			if round1Packages[i] == nil {
				round1Packages[i] = &pb.PackageMap{
					Packages: make(map[string][]byte),
				}
			}
			round1Packages[i].Packages[round1Response.Identifier] = p
		}
	}

	// Round 1 Validation
	round1Signatures := make(map[string][]byte)

	for _, client := range clientMap {
		round1SignatureRequest := &pb.Round1PackagesRequest{
			RequestId:      requestIdString,
			Round1Packages: round1Packages,
		}
		round1SignatureResponse, err := client.Round1Packages(context.Background(), round1SignatureRequest)
		if err != nil {
			return err
		}
		round1Signatures[round1SignatureResponse.Identifier] = round1SignatureResponse.Round1Signature
	}

	wg := sync.WaitGroup{}

	// Round 1 Signature Delivery
	for _, client := range clientMap {
		wg.Add(1)
		go func(client pb.DKGServiceClient) {
			defer wg.Done()
			round1SignatureRequest := &pb.Round1SignatureRequest{
				RequestId:        requestIdString,
				Round1Signatures: round1Signatures,
			}
			round1SignatureResponse, err := client.Round1Signature(context.Background(), round1SignatureRequest)
			if err != nil {
				return
			}

			if len(round1SignatureResponse.ValidationFailures) > 0 {
				return
			}
		}(client)
	}

	wg.Wait()

	return nil
}
