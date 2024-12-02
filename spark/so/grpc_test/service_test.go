package grpctest

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

func TestGenerateDepositAddress(t *testing.T) {
	conn, err := common.NewGRPCConnection("localhost:8535")
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)

	pubkey, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	if err != nil {
		t.Fatalf("failed to decode public key: %v", err)
	}

	resp, err := client.GenerateDepositAddress(context.Background(), &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  pubkey,
		IdentityPublicKey: pubkey,
	})
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	if resp.Address == "" {
		t.Fatalf("deposit address is empty")
	}
}
