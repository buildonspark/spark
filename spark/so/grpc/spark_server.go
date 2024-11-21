package grpc

import (
	"context"
	"fmt"
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
	keyshares, err := ent_utils.GetUnusedSigningKeyshares(ctx, s.config, 1)
	if err != nil {
		return nil, err
	}

	if len(keyshares) == 0 {
		return nil, fmt.Errorf("no keyshares available")
	}

	keyshare := keyshares[0]

	err = ent_utils.MarkSigningKeysharesAsUsed(ctx, s.config, []uuid.UUID{keyshare.ID})
	if err != nil {
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
			conn, err := common.NewGRPCConnection(operator.Address)
			if err != nil {
				results <- err
				return
			}

			client := pb.NewSparkInternalServiceClient(conn)
			_, err = client.MarkKeysharesAsUsed(ctx, &pb.MarkKeysharesAsUsedRequest{KeyshareId: []string{keyshare.ID.String()}})
			results <- err

			wg.Done()
		}(operator)
	}

	wg.Wait()

	var errors []error
	for err := range results {
		if err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		return nil, fmt.Errorf("failed to mark keyshares as used on all operators: %v", errors)
	}

	combinedPublicKey, err := common.AddPublicKeys(keyshare.PublicKey, req.PublicKey)
	if err != nil {
		return nil, err
	}

	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, s.config.Network)
	if err != nil {
		return nil, err
	}

	// TODO: we need to store the deposit address along with the keyshare ID.

	return &pb.GenerateDepositAddressResponse{Address: *depositAddress}, nil
}
