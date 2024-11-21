package grpc

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent_utils"
)

type SparkServer struct {
	pb.UnimplementedSparkServiceServer
	config *so.Config
}

func NewSparkServer(config *so.Config) *SparkServer {
	return &SparkServer{config: config}
}

func (s *SparkServer) GenerateDepositAddress(ctx context.Context, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	log.Printf("Generating deposit address for public key: %s", hex.EncodeToString(req.PublicKey))
	keyshares, err := ent_utils.GetUnusedSigningKeyshares(ctx, s.config, 1)
	if err != nil {
		return nil, err
	}

	if len(keyshares) == 0 {
		log.Printf("No keyshares available")
		return nil, fmt.Errorf("no keyshares available")
	}

	keyshare := keyshares[0]

	err = ent_utils.MarkSigningKeysharesAsUsed(ctx, s.config, []uuid.UUID{keyshare.ID})
	if err != nil {
		log.Printf("Failed to mark keyshare as used: %v", err)
		return nil, err
	}

	results := make(chan error, len(s.config.SigningOperatorMap)-1)

	wg := sync.WaitGroup{}

	for _, operator := range s.config.SigningOperatorMap {
		if operator.Identifier == s.config.Identifier {
			continue
		}

		wg.Add(1)

		go func(operator *so.SigningOperator) {
			defer wg.Done()
			log.Printf("Connecting to operator: %s", operator.Address)
			conn, err := common.NewGRPCConnection(operator.Address)
			if err != nil {
				results <- err
				log.Printf("Failed to connect to operator: %v", err)
				return
			}

			client := pb.NewSparkInternalServiceClient(conn)
			_, err = client.MarkKeysharesAsUsed(ctx, &pb.MarkKeysharesAsUsedRequest{KeyshareId: []string{keyshare.ID.String()}})
			results <- err
			log.Printf("Marked keyshare as used on operator: %s", operator.Address)
		}(operator)
	}

	log.Printf("Waiting for all operators to mark keyshare as used")
	wg.Wait()
	close(results)

	var errors []error
	for err := range results {
		if err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		log.Printf("Failed to mark keyshares as used on all operators: %v", errors)
		return nil, fmt.Errorf("failed to mark keyshares as used on all operators: %v", errors)
	}

	combinedPublicKey, err := common.AddPublicKeys(keyshare.PublicKey, req.PublicKey)
	if err != nil {
		log.Printf("Failed to add public keys: %v", err)
		return nil, err
	}

	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, s.config.Network)
	if err != nil {
		log.Printf("Failed to generate deposit address: %v", err)
		return nil, err
	}

	// TODO: we need to store the deposit address along with the keyshare ID.

	log.Printf("Generated deposit address: %s", depositAddress)
	return &pb.GenerateDepositAddressResponse{Address: *depositAddress}, nil
}
