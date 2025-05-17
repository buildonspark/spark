package grpctest

import (
	"bytes"
	"context"
	"encoding/hex"
	"log"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent/schema"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/assert"
)

func TestTreeCreationAddressGeneration(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	// Setup Mock tx
	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, &leafID, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	depositTx, err := testutil.CreateTestP2TRTransaction(depositResp.DepositAddress.Address, 65536)
	if err != nil {
		t.Fatalf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		t.Fatalf("failed to serialize deposit tx: %v", err)
	}
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	if err != nil {
		t.Fatalf("failed to decode deposit tx hex: %v", err)
	}
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	if err != nil {
		t.Fatalf("failed to deserilize deposit tx: %v", err)
	}

	log.Printf("deposit public key: %x", hex.EncodeToString(privKey.PubKey().SerializeCompressed()))
	tree, err := wallet.GenerateDepositAddressesForTree(ctx, config, depositTx, nil, uint32(vout), privKey.Serialize(), 3)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	log.Printf("tree created: %v", tree)

	treeNodes, err := wallet.CreateTree(ctx, config, depositTx, nil, uint32(vout), tree, true)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	log.Printf("tree nodes created: %v", treeNodes)
}

func TestTreeCreationWithMultiLevels(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, &leafID, false)
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	client, err := testutil.NewRegtestClient()
	assert.NoError(t, err)

	coin, err := faucet.Fund()
	assert.NoError(t, err)
	depositTx, err := testutil.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, 65536)
	if err != nil {
		t.Fatalf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		t.Fatalf("failed to serialize deposit tx: %v", err)
	}
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	if err != nil {
		t.Fatalf("failed to decode deposit tx hex: %v", err)
	}
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	if err != nil {
		t.Fatalf("failed to deserilize deposit tx: %v", err)
	}

	log.Printf("deposit public key: %x", hex.EncodeToString(privKey.PubKey().SerializeCompressed()))
	tree, err := wallet.GenerateDepositAddressesForTree(ctx, config, depositTx, nil, uint32(vout), privKey.Serialize(), 2)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	log.Printf("tree created: %v", tree)

	treeNodes, err := wallet.CreateTree(ctx, config, depositTx, nil, uint32(vout), tree, false)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	assert.Equal(t, len(treeNodes.Nodes), 3)

	for i, node := range treeNodes.Nodes {
		if i == 0 {
			continue
		}
		leftPrivKeyBytes := tree.Children[i-1].Children[0].SigningPrivateKey
		leftAddress, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, node, 0, leftPrivKeyBytes, 2)
		if err != nil {
			t.Fatalf("failed to create tree: %v", err)
		}
		_, err = wallet.CreateTree(ctx, config, nil, node, 0, leftAddress, true)
		if err != nil {
			t.Fatalf("failed to create tree: %v", err)
		}

		rightPrivKeyBytes := tree.Children[i-1].Children[1].SigningPrivateKey
		rightAddress, err := wallet.GenerateDepositAddressesForTree(ctx, config, nil, node, 1, rightPrivKeyBytes, 2)
		if err != nil {
			t.Fatalf("failed to create tree: %v", err)
		}
		_, err = wallet.CreateTree(ctx, config, nil, node, 1, rightAddress, true)
		if err != nil {
			t.Fatalf("failed to create tree: %v", err)
		}

	}

	for _, node := range treeNodes.Nodes {
		assert.Equal(t, node.Status, string(schema.TreeNodeStatusCreating))
	}

	// Sign, broadcast, and mine deposit tx
	signedDepositTx, err := testutil.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	log.Printf("signed deposit tx: %s", signedDepositTx.TxHash().String())
	assert.NoError(t, err)
	_, err = client.SendRawTransaction(signedDepositTx, true)
	assert.NoError(t, err)

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	assert.NoError(t, err)

	time.Sleep(2 * time.Second)

	sparkClient := pb.NewSparkServiceClient(conn)
	response, err := sparkClient.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: config.IdentityPublicKey()},
		IncludeParents: true,
	})
	assert.NoError(t, err)
	assert.Greater(t, len(response.Nodes), 0)
}
