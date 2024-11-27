package grpc

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent_utils"
	"github.com/lightsparkdev/spark-go/so/helper"
)

type SparkServer struct {
	pb.UnimplementedSparkServiceServer
	config *so.Config
}

func NewSparkServer(config *so.Config) *SparkServer {
	return &SparkServer{config: config}
}

func (s *SparkServer) GenerateDepositAddress(ctx context.Context, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	log.Printf("Generating deposit address for public key: %s", hex.EncodeToString(req.SigningPublicKey))
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

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, s.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			log.Printf("Failed to connect to operator: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pb.NewSparkInternalServiceClient(conn)
		_, err = client.MarkKeysharesAsUsed(ctx, &pb.MarkKeysharesAsUsedRequest{KeyshareId: []string{keyshare.ID.String()}})
		return nil, err
	})
	if err != nil {
		log.Printf("Failed to execute task with all operators: %v", err)
		return nil, err
	}

	combinedPublicKey, err := common.AddPublicKeys(keyshare.PublicKey, req.SigningPublicKey)
	if err != nil {
		log.Printf("Failed to add public keys: %v", err)
		return nil, err
	}

	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, s.config.Network)
	if err != nil {
		log.Printf("Failed to generate deposit address: %v", err)
		return nil, err
	}

	_, err = common.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetOwnerIdentityPubkey(req.IdentityPublicKey).
		SetAddress(*depositAddress).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to link keyshare to deposit address: %v", err)
		return nil, err
	}

	_, err = helper.ExecuteTaskWithAllOperators(ctx, s.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			log.Printf("Failed to connect to operator: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pb.NewSparkInternalServiceClient(conn)
		_, err = client.MarkKeyshareForDepositAddress(ctx, &pb.MarkKeyshareForDepositAddressRequest{
			KeyshareId:             keyshare.ID.String(),
			Address:                *depositAddress,
			OwnerIdentityPublicKey: req.IdentityPublicKey,
		})
		return nil, err
	})
	if err != nil {
		log.Printf("Failed to execute task with all operators: %v", err)
		return nil, err
	}

	log.Printf("Generated deposit address: %s", *depositAddress)
	return &pb.GenerateDepositAddressResponse{Address: *depositAddress}, nil
}
