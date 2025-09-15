package grpctest

import (
	"maps"
	"slices"
	"testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

func TestTreeQuery(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)

	// Create gRPC connection using common helper
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	// Authenticate the connection
	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	if err != nil {
		t.Fatalf("Failed to authenticate: %v", err)
	}

	ctx := wallet.ContextWithToken(t.Context(), token)
	client := pb.NewSparkServiceClient(conn)

	// Create test nodes with parent chain
	rootPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

	tree, err := sparktesting.CreateNewTree(config, faucet, rootPrivKey, 65536)
	require.NoError(t, err)

	// Generate tree structure for root with 2 levels
	rootTree, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, tree, uint32(0), rootPrivKey, 1)
	require.NoError(t, err)

	// Create initial tree with 2 levels
	treeNodes, err := wallet.CreateTree(ctx, config, nil, tree, uint32(0), rootTree, true)
	require.NoError(t, err)
	require.Len(t, treeNodes.Nodes, 5) // Root + 2 children + 2 leaves

	leafNode := treeNodes.Nodes[1]

	network, err := common.ProtoNetworkFromNetwork(config.Network)
	require.NoError(t, err, "failed to get proto network")

	t.Run("query by owner identity key", func(t *testing.T) {
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: leafNode.OwnerIdentityPublicKey},
			IncludeParents: true,
			Network:        network,
		}

		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 6)
	})

	t.Run("query by owner identity key for regtest wallet returns empty for mainnet", func(t *testing.T) {
		networkMainnet := pb.Network_MAINNET
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: leafNode.OwnerIdentityPublicKey},
			IncludeParents: true,
			Network:        networkMainnet,
		}

		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Empty(t, resp.Nodes)
	})

	t.Run("query without network defaults to mainnet and returns empty for regtest test wallet", func(t *testing.T) {
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: leafNode.OwnerIdentityPublicKey},
			IncludeParents: true,
		}

		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Empty(t, resp.Nodes)
	})

	t.Run("query with paginations", func(t *testing.T) {
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: leafNode.OwnerIdentityPublicKey},
			IncludeParents: false,
			Network:        network,
		}
		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 2)
		require.Zero(t, resp.Offset)

		// We return these in a map from the SO, which has an undefined ordering in protobufs, so sort
		// them to ensure our checks later are deterministic.
		nodeIDs := slices.Sorted(maps.Keys(resp.Nodes))
		slices.Reverse(nodeIDs)

		req.Limit = 1
		resp, err = client.QueryNodes(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 1)
		require.Equal(t, 1, int(resp.Offset))
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
			Network:        network,
		}

		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)

		require.Len(t, resp.Nodes, 1)
		node, exists := resp.Nodes[leafNode.Id]
		require.True(t, exists)
		require.NotEmpty(t, node.SigningKeyshare.PublicKey)
	})

	t.Run("query by node id with parents", func(t *testing.T) {
		req := &pb.QueryNodesRequest{
			Source:         &pb.QueryNodesRequest_NodeIds{NodeIds: &pb.TreeNodeIds{NodeIds: []string{leafNode.Id}}},
			IncludeParents: true,
			Network:        network,
		}

		resp, err := client.QueryNodes(ctx, req)
		require.NoError(t, err)

		require.Len(t, resp.Nodes, 3)
		require.Contains(t, resp.Nodes, leafNode.Id)
		require.Contains(t, resp.Nodes, treeNodes.Nodes[0].Id)
	})

	t.Run("query nodes distribution", func(t *testing.T) {
		req := &pb.QueryNodesDistributionRequest{
			OwnerIdentityPublicKey: leafNode.OwnerIdentityPublicKey,
		}

		resp, err := client.QueryNodesDistribution(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.NodeDistribution, 1)
		t.Logf("resp.NodeDistribution: %v", resp.NodeDistribution)
		for _, v := range resp.NodeDistribution {
			require.Equal(t, uint64(2), v)
		}
	})

	t.Run("query nodes by value", func(t *testing.T) {
		rootPrivKey2, err := keys.GeneratePrivateKey()
		require.NoError(t, err)
		tree2, err := sparktesting.CreateNewTree(config, faucet, rootPrivKey2, 32768)
		require.NoError(t, err)
		rootTree2, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, tree2, uint32(0), rootPrivKey2, 1)
		require.NoError(t, err)
		tree2Nodes, err := wallet.CreateTree(ctx, config, nil, tree2, uint32(0), rootTree2, true)
		require.NoError(t, err)
		require.Len(t, tree2Nodes.Nodes, 5)

		req := &pb.QueryNodesByValueRequest{
			OwnerIdentityPublicKey: leafNode.OwnerIdentityPublicKey,
			Limit:                  1,
			Offset:                 0,
			Value:                  32768,
		}
		resp, err := client.QueryNodesByValue(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 1)
		require.Equal(t, 1, int(resp.Offset))

		req.Offset = 1
		resp, err = client.QueryNodesByValue(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 1)
		require.Equal(t, 2, int(resp.Offset))

		req.Offset = 2
		resp, err = client.QueryNodesByValue(ctx, req)
		require.NoError(t, err)
		require.Empty(t, resp.Nodes)
		require.Equal(t, -1, int(resp.Offset))

		req.Value = 16384
		req.Offset = 0
		resp, err = client.QueryNodesByValue(ctx, req)
		require.NoError(t, err)
		require.Len(t, resp.Nodes, 1)
		require.Equal(t, 1, int(resp.Offset))
	})
}
