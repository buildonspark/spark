package wallet

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

func QueryUserSignedRefunds(ctx context.Context, config *Config, paymentHash []byte) ([]*pb.UserSignedRefund, error) {
	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		return nil, fmt.Errorf("unable to connect to coordinator: %v", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	client := pb.NewSparkServiceClient(conn)

	request := &pb.QueryUserSignedRefundsRequest{
		PaymentHash:       paymentHash,
		IdentityPublicKey: config.IdentityPublicKey(),
	}

	response, err := client.QueryUserSignedRefunds(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to query user signed refunds: %v", err)
	}
	return response.UserSignedRefunds, nil
}

func ValidateUserSignedRefund(userSignedRefund *pb.UserSignedRefund) (int64, error) {
	// TODO: Validate the signed refund from user's public key
	refundTx, err := common.TxFromRawTxBytes(userSignedRefund.RefundTx)
	if err != nil {
		return 0, fmt.Errorf("failed to parse refund transaction: %v", err)
	}

	return refundTx.TxOut[0].Value, nil
}

func ProvidePreimage(ctx context.Context, config *Config, preimage []byte) (*pb.Transfer, error) {
	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		return nil, fmt.Errorf("unable to connect to coordinator: %v", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	client := pb.NewSparkServiceClient(conn)

	paymentHash := sha256.Sum256(preimage)

	request := &pb.ProvidePreimageRequest{
		Preimage:          preimage,
		PaymentHash:       paymentHash[:],
		IdentityPublicKey: config.IdentityPublicKey(),
	}

	response, err := client.ProvidePreimage(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to provide preimage: %v", err)
	}

	return response.Transfer, nil
}
