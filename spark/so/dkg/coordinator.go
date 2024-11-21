package dkg

import (
	"context"
	"log"
	"sync"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
)

func RunDKGIfNeeded(config *so.Config) error {
	dbClient, err := ent.Open(config.DatabaseDriver(), config.DatabasePath+"?_fk=1")
	if err != nil {
		return err
	}
	defer dbClient.Close()

	count, err := dbClient.SigningKeyshare.Query().Where(
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
	clientMap := make(map[string]*DkgClient)
	for identifier, operator := range config.SigningOperatorMap {
		client, err := NewDKGServiceClient(operator.Address)
		if err != nil {
			return err
		}
		clientMap[identifier] = client
	}

	// Initiate DKG
	requestId, err := uuid.NewV7()
	if err != nil {
		return err
	}
	requestIdString := requestId.String()
	initRequest := &pb.InitiateDkgRequest{
		RequestId:  requestIdString,
		KeyCount:   keyCount,
		MinSigners: config.Threshold,
		MaxSigners: uint64(len(config.SigningOperatorMap)),
	}

	round1Packages := make([]*pb.PackageMap, int(keyCount))

	for identifier, client := range clientMap {
		log.Printf("Initiating DKG with %s", identifier)
		round1Response, err := client.Client.InitiateDkg(context.Background(), initRequest)
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
		round1SignatureResponse, err := client.Client.Round1Packages(context.Background(), round1SignatureRequest)
		if err != nil {
			return err
		}
		round1Signatures[round1SignatureResponse.Identifier] = round1SignatureResponse.Round1Signature
	}

	wg := sync.WaitGroup{}

	// Round 1 Signature Delivery
	for _, client := range clientMap {
		wg.Add(1)
		go func(client *DkgClient) {
			defer wg.Done()
			round1SignatureRequest := &pb.Round1SignatureRequest{
				RequestId:        requestIdString,
				Round1Signatures: round1Signatures,
			}
			round1SignatureResponse, err := client.Client.Round1Signature(context.Background(), round1SignatureRequest)
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
