package grpctest

import (
	"bytes"
	"encoding/hex"
	"log"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeCreationAddressGeneration(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	// Setup Mock tx
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to connect to operator")
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err, "failed to authenticate")
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	require.NoError(t, err, "failed to generate deposit address")

	depositTx, err := sparktesting.CreateTestP2TRTransaction(depositResp.DepositAddress.Address, 65536)
	require.NoError(t, err)
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	require.NoError(t, err)
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	require.NoError(t, err)
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	require.NoError(t, err)

	log.Printf("deposit public key: %s", privKey.Public().ToHex())
	tree, err := wallet.GenerateDepositAddressesForTree(ctx, config, depositTx, nil, uint32(vout), privKey, 3)
	require.NoError(t, err)

	log.Printf("tree created: %v", tree)

	treeNodes, err := wallet.CreateTree(ctx, config, depositTx, nil, uint32(vout), tree, true)
	require.NoError(t, err)

	log.Printf("tree nodes created: %v", treeNodes)
}

func TestTreeCreationWithMultiLevels(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	require.NoError(t, err)

	client := sparktesting.GetBitcoinClient()

	coin, err := faucet.Fund()
	require.NoError(t, err)
	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 65536)
	require.NoError(t, err)
	vout := 0
	var buf bytes.Buffer
	require.NoError(t, depositTx.Serialize(&buf))
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	require.NoError(t, err)
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	require.NoError(t, err)

	log.Printf("deposit public key: %x", hex.EncodeToString(privKey.Public().Serialize()))
	tree, err := wallet.GenerateDepositAddressesForTree(ctx, config, depositTx, nil, uint32(vout), privKey, 2)
	require.NoError(t, err)

	log.Printf("tree created: %v", tree)

	treeNodes, err := wallet.CreateTree(ctx, config, depositTx, nil, uint32(vout), tree, false)
	require.NoError(t, err)

	assert.Len(t, treeNodes.Nodes, 3)

	for i, node := range treeNodes.Nodes {
		if i == 0 {
			continue
		}
		leftPrivKeyBytes := tree.Children[i-1].Children[0].SigningPrivateKey
		leftAddress, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, node, 0, leftPrivKeyBytes, 2)
		require.NoError(t, err)
		_, err = wallet.CreateTree(ctx, config, nil, node, 0, leftAddress, true)
		require.NoError(t, err)

		rightPrivKeyBytes := tree.Children[i-1].Children[1].SigningPrivateKey
		rightAddress, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, node, 1, rightPrivKeyBytes, 2)
		require.NoError(t, err)
		_, err = wallet.CreateTree(ctx, config, nil, node, 1, rightAddress, true)
		require.NoError(t, err)

	}

	for _, node := range treeNodes.Nodes {
		assert.Equal(t, string(st.TreeNodeStatusCreating), node.Status)
	}

	// Sign, broadcast, and mine deposit tx
	signedDepositTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	require.NoError(t, err)
	log.Printf("signed deposit tx: %s", signedDepositTx.TxHash().String())
	_, err = client.SendRawTransaction(signedDepositTx, true)
	require.NoError(t, err)

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	require.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	sparkClient := pb.NewSparkServiceClient(conn)

	network, err := common.ProtoNetworkFromNetwork(config.Network)
	require.NoError(t, err)
	response, err := sparkClient.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: config.IdentityPublicKey().Serialize()},
		IncludeParents: true,
		Network:        network,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, response.Nodes)
}

func TestTreeCreationSplitMultipleTimes(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)

	privKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	require.NoError(t, err)

	depositTx, err := sparktesting.CreateTestP2TRTransaction(depositResp.DepositAddress.Address, 65536)
	require.NoError(t, err)
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	require.NoError(t, err)
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	require.NoError(t, err)
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	require.NoError(t, err)

	log.Printf("deposit public key: %x", privKey.Public().ToHex())
	tree, err := wallet.GenerateDepositAddressesForTree(ctx, config, depositTx, nil, uint32(vout), privKey, 2)
	require.NoError(t, err)

	log.Printf("tree created: %v", tree)

	treeNodes, err := wallet.CreateTree(ctx, config, depositTx, nil, uint32(vout), tree, false)
	require.NoError(t, err)

	assert.Len(t, treeNodes.Nodes, 3)

	_, err = wallet.GenerateDepositAddressesForTree(ctx, config, depositTx, nil, uint32(vout), privKey, 2)
	require.Error(t, err)

	for i, node := range treeNodes.Nodes {
		if i == 0 {
			continue
		}
		leftPrivKeyBytes := tree.Children[i-1].Children[0].SigningPrivateKey
		leftAddress, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, node, 0, leftPrivKeyBytes, 2)
		require.NoError(t, err)
		_, err = wallet.CreateTree(ctx, config, nil, node, 0, leftAddress, true)
		require.NoError(t, err)

		rightPrivKeyBytes := tree.Children[i-1].Children[1].SigningPrivateKey
		rightAddress, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, node, 1, rightPrivKeyBytes, 2)
		require.NoError(t, err)
		_, err = wallet.CreateTree(ctx, config, nil, node, 1, rightAddress, true)
		require.NoError(t, err)
	}

	for i, node := range treeNodes.Nodes {
		if i == 0 {
			continue
		}
		leftPrivKeyBytes := tree.Children[i-1].Children[0].SigningPrivateKey
		// Check that, if you create a tree on a leaf that has already created the subtree, it will fail the call
		_, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, node, 0, leftPrivKeyBytes, 2)
		require.Error(t, err)

		rightPrivKeyBytes := tree.Children[i-1].Children[1].SigningPrivateKey
		_, err = wallet.GenerateDepositAddressesForTree(ctx, config, nil, node, 1, rightPrivKeyBytes, 2)
		require.Error(t, err)
	}
}
