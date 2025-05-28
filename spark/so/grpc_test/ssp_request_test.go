package grpctest

import (
	"context"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	mock "github.com/lightsparkdev/spark/proto/mock"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/stretchr/testify/require"
)

func TestQueryLostNodes(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	priKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to create node signing private key")
	root, err := testutil.CreateNewTree(config, faucet, priKey, 100_000)
	require.NoError(t, err, "failed to create new tree")

	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	require.NoError(t, err, "failed to create gRPC connection")
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)
	network, err := common.ProtoNetworkFromNetwork(config.Network)
	require.NoError(t, err, "failed to get proto network")
	resp, err := client.QueryNodes(context.Background(), &pb.QueryNodesRequest{
		Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: root.OwnerIdentityPublicKey},
		IncludeParents: false,
		Network:        network,
	})
	require.NoError(t, err)
	require.Len(t, resp.Nodes, 1)
	leafID := ""
	for _, node := range resp.Nodes {
		leafID = node.Id
	}

	sparkSspClient := pbssp.NewSparkSspInternalServiceClient(conn)
	response, err := sparkSspClient.QueryLostNodes(context.Background(), &pbssp.QueryLostNodesRequest{})
	require.NoError(t, err, "failed to query lost nodes")
	require.Len(t, response.Nodes, 0)

	mockClient := mock.NewMockServiceClient(conn)
	_, err = mockClient.UpdateNodesStatus(context.Background(), &mock.UpdateNodesStatusRequest{
		NodeIds: []string{leafID},
		Status:  "LOST",
	})
	require.NoError(t, err, "failed to mark node LOST")

	response, err = sparkSspClient.QueryLostNodes(context.Background(), &pbssp.QueryLostNodesRequest{})
	require.NoError(t, err, "failed to query lost nodes")
	require.Len(t, response.Nodes, 1)
}
