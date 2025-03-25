package tree_test

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark_tree"
)

func TestGetLeafDenominationCounts(t *testing.T) {
	conn, err := common.NewGRPCConnectionWithTestTLS("localhost:8535", nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	client := pb.NewSparkTreeServiceClient(conn)

	userPubkey, _ := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	req := &pb.GetLeafDenominationCountsRequest{OwnerIdentityPublicKey: userPubkey}
	resp, err := client.GetLeafDenominationCounts(context.Background(), req)
	if err != nil {
		t.Fatalf("failed to get leaf denomination counts: %v", err)
	}
	t.Logf("leaf denomination counts: %v", resp.Counts)
}
