package wallet

import (
	"context"
	"crypto/sha256"
	"fmt"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

func ProvidePreimage(ctx context.Context, config *TestWalletConfig, preimage []byte) (*pb.Transfer, error) {
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	client := pb.NewSparkServiceClient(conn)

	paymentHash := sha256.Sum256(preimage)

	request := &pb.ProvidePreimageRequest{
		Preimage:          preimage,
		PaymentHash:       paymentHash[:],
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
	}

	response, err := client.ProvidePreimage(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to provide preimage: %w", err)
	}

	return response.GetTransfer(), nil
}
