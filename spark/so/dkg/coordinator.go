package dkg

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
)

func GenerateKeys(config *so.Config, keyCount uint64, threshold uint64) error {
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
	requestId := uuid.New().String()
	initRequest := &pb.InitiateDkgRequest{
		RequestId: requestId,
		KeyCount:  keyCount,
		MinSigners: threshold,
		MaxSigners: uint64(len(config.SigningOperatorMap)),
	}

	round1Packages := make([]*pb.PackageMap, int(keyCount))

	for _, client := range clientMap {
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
			RequestId: requestId,
			Round1Packages: round1Packages,
		}
		round1SignatureResponse, err := client.Client.Round1Packages(context.Background(), round1SignatureRequest)
		if err != nil {
			return err
		}
		round1Signatures[round1SignatureResponse.Identifier] = round1SignatureResponse.Round1Signature
	}

	// Round 1 Signature Delivery
	for _, client := range clientMap {
		round1SignatureRequest := &pb.Round1SignatureRequest{
			RequestId:        requestId,
			Round1Signatures: round1Signatures,
		}
		round1SignatureResponse, err := client.Client.Round1Signature(context.Background(), round1SignatureRequest)
		if err != nil {
			return err
		}

		if len(round1SignatureResponse.ValidationFailures) > 0 {
			return fmt.Errorf("validation failures: %v", round1SignatureResponse.ValidationFailures)
		}
	}

	return nil
}
