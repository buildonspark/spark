package wallet

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

func QueryUserSignedRefunds(ctx context.Context, config *TestWalletConfig, paymentHash []byte) ([]*pb.UserSignedRefund, error) {
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

	request := &pb.QueryUserSignedRefundsRequest{
		PaymentHash:       paymentHash,
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
	}

	response, err := client.QueryUserSignedRefunds(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to query user signed refunds: %w", err)
	}
	return response.GetUserSignedRefunds(), nil
}

func ValidateUserSignedRefund(userSignedRefund *pb.UserSignedRefund) (int64, error) {
	// TODO: Validate the signed refund from user's public key
	refundTx, err := common.TxFromRawTxBytes(userSignedRefund.GetRefundTx())
	if err != nil {
		return 0, fmt.Errorf("failed to parse refund transaction: %w", err)
	}

	return refundTx.TxOut[0].Value, nil
}

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
