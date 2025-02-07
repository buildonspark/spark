package grpctest

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/require"
)

func TestTreeQuery(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	// Create gRPC connection using common helper
	conn, err := common.NewGRPCConnection(config.CoodinatorAddress())
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

	// Setup mock client for transaction verification
	mockClient := pbmock.NewMockServiceClient(conn)

	// Create test nodes with parent chain
	rootPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	rootPubKeyBytes := rootPrivKey.PubKey().SerializeCompressed()

	// Generate deposit address using wallet helper
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, rootPubKeyBytes)
	require.NoError(t, err)

	// Create deposit transaction with value
	const txValue = int64(65536)
	depositTx, err := testutil.CreateTestP2TRTransaction(depositResp.DepositAddress.Address, txValue)
	require.NoError(t, err)

	// Mock the transaction in the test environment
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	require.NoError(t, err)
	depositTxHex := hex.EncodeToString(buf.Bytes())

	_, err = mockClient.SetMockOnchainTx(context.Background(), &pbmock.SetMockOnchainTxRequest{
		Txid: depositTx.TxHash().String(),
		Tx:   depositTxHex,
	})
	require.NoError(t, err)

	// Generate tree structure for root with 2 levels first
	rootTree, err := wallet.GenerateDepositAddressesForTree(ctx, config, depositTx, nil, uint32(0), rootPrivKey.Serialize(), 2)
	require.NoError(t, err)

	// Create initial tree with 2 levels
	treeNodes, err := wallet.CreateTree(ctx, config, depositTx, nil, uint32(0), rootTree, false)
	require.NoError(t, err)
	require.Len(t, treeNodes.Nodes, 3) // Root + 2 children

	// Store the root node
	rootNode := treeNodes.Nodes[0]

	t.Run("query root node", func(t *testing.T) {
		req := &pb.TreeNodesByPublicKeyRequest{
			OwnerIdentityPubkey: rootNode.GetOwnerIdentityPublicKey(),
		}

		resp, err := client.GetTreeNodesByPublicKey(ctx, req)
		require.NoError(t, err)

		require.Len(t, resp.Nodes, 3)
		require.Equal(t, rootNode.GetId(), resp.Nodes[0].Id)
	})

	t.Run("query leaf node", func(t *testing.T) {
		req := &pb.TreeNodesByPublicKeyRequest{
			OwnerIdentityPubkey: treeNodes.Nodes[1].GetOwnerIdentityPublicKey(),
		}

		resp, err := client.GetTreeNodesByPublicKey(ctx, req)
		require.NoError(t, err)

		require.Len(t, resp.Nodes, 3)
		require.Equal(t, rootNode.GetId(), resp.Nodes[0].Id)
	})
}
