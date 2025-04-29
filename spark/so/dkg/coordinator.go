package dkg

import (
	"context"
	"sync"

	"github.com/google/uuid"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	"github.com/lightsparkdev/spark/so"
)

// GenerateKeys runs the DKG protocol to generate the keys.
func GenerateKeys(ctx context.Context, config *so.Config, keyCount uint64) error {
	// Init clients
	clientMap := make(map[string]pbdkg.DKGServiceClient)
	for identifier, operator := range config.SigningOperatorMap {
		connection, err := operator.NewGRPCConnection()
		if err != nil {
			return err
		}
		defer connection.Close()
		client := pbdkg.NewDKGServiceClient(connection)
		clientMap[identifier] = client
	}

	// Initiate DKG
	requestID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	requestIDString := requestID.String()
	initRequest := &pbdkg.InitiateDkgRequest{
		RequestId:        requestIDString,
		KeyCount:         keyCount,
		MinSigners:       config.Threshold,
		MaxSigners:       uint64(len(config.SigningOperatorMap)),
		CoordinatorIndex: config.Index,
	}

	round1Packages := make([]*pbcommon.PackageMap, int(keyCount))

	for _, client := range clientMap {
		round1Response, err := client.InitiateDkg(ctx, initRequest)
		if err != nil {
			return err
		}
		for i, p := range round1Response.Round1Package {
			if round1Packages[i] == nil {
				round1Packages[i] = &pbcommon.PackageMap{
					Packages: make(map[string][]byte),
				}
			}
			round1Packages[i].Packages[round1Response.Identifier] = p
		}
	}

	// Round 1 Validation
	round1Signatures := make(map[string][]byte)

	for _, client := range clientMap {
		round1SignatureRequest := &pbdkg.Round1PackagesRequest{
			RequestId:      requestIDString,
			Round1Packages: round1Packages,
		}
		round1SignatureResponse, err := client.Round1Packages(ctx, round1SignatureRequest)
		if err != nil {
			return err
		}
		round1Signatures[round1SignatureResponse.Identifier] = round1SignatureResponse.Round1Signature
	}

	wg := sync.WaitGroup{}

	// Round 1 Signature Delivery
	for _, client := range clientMap {
		wg.Add(1)
		go func(client pbdkg.DKGServiceClient) {
			defer wg.Done()
			round1SignatureRequest := &pbdkg.Round1SignatureRequest{
				RequestId:        requestIDString,
				Round1Signatures: round1Signatures,
			}
			round1SignatureResponse, err := client.Round1Signature(ctx, round1SignatureRequest)
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
