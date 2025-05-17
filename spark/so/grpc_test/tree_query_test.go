package grpctest

import (
	"context"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/require"
)

func TestTreeQuery(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	// Create gRPC connection using common helper
	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	// Authenticate the connection
	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		t.Fatalf("Failed to authenticate: %v", err)
	}

	ctx := wallet.ContextWithToken(context.Background(), token)
	client := pb.NewSparkServiceClient(conn)

	// Create test nodes with parent chain
	rootPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	tree, err := testutil.CreateNewTree(config, faucet, rootPrivKey, 65536)
	require.NoError(t, err)

	// Generate tree structure for root with 2 levels
	rootTree, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, tree, uint32(0), rootPrivKey.Serialize(), 1)
	require.NoError(t, err)

	// Create initial tree with 2 levels
	treeNodes, err := wallet.CreateTree(ctx, config, nil, tree, uint32(0), rootTree, true)
	require.NoError(t, err)
	require.Len(t, treeNodes.Nodes, 5) // Root + 2 children + 2 leaves

	leafNode := treeNodes.Nodes[1]

	t.Run("query by owner identity key", func(t *testing.T) {
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: leafNode.OwnerIdentityPublicKey},
			IncludeParents: true,
		}

		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 6)
	})

	t.Run("query with paginations", func(t *testing.T) {
		nodeIDs := make([]string, 0, 2)
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: leafNode.OwnerIdentityPublicKey},
			IncludeParents: false,
		}
		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 2)
		require.Equal(t, int(resp.Offset), 0)
		for id := range resp.Nodes {
			nodeIDs = append(nodeIDs, id)
		}

		req.Limit = 1
		resp, err = client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 1)
		require.Equal(t, int(resp.Offset), 1)
		for id := range resp.Nodes {
			require.Equal(t, id, nodeIDs[0])
		}

		req.Limit = 2
		req.Offset = resp.Offset
		resp, err = client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 1)
		require.Equal(t, int(resp.Offset), -1)
		for id := range resp.Nodes {
			require.Equal(t, id, nodeIDs[1])
		}
	})

	t.Run("query by node id without parents", func(t *testing.T) {
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_NodeIds{NodeIds: &pb.TreeNodeIds{NodeIds: []string{leafNode.Id}}},
			IncludeParents: false,
		}

		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)

		require.Len(t, resp.Nodes, 1)
		_, exists := resp.Nodes[leafNode.Id]
		require.True(t, exists)
	})

	t.Run("query by node id with parents", func(t *testing.T) {
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_NodeIds{NodeIds: &pb.TreeNodeIds{NodeIds: []string{leafNode.Id}}},
			IncludeParents: true,
		}

		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)

		require.Len(t, resp.Nodes, 3)
		_, exists := resp.Nodes[leafNode.Id]
		require.True(t, exists)
		_, exists = resp.Nodes[treeNodes.Nodes[0].Id]
		require.True(t, exists)
	})
}
