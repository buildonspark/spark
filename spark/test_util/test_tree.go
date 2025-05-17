package testutil

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/wallet"
)

const (
	DepositTimeout      = 30 * time.Second
	DepositPollInterval = 100 * time.Millisecond
)

func WaitForPendingDepositNode(ctx context.Context, sparkClient pb.SparkServiceClient, node *pb.TreeNode) (*pb.TreeNode, error) {
	startTime := time.Now()
	for node.Status != string(schema.TreeNodeStatusAvailable) {
		if time.Since(startTime) >= DepositTimeout {
			return nil, fmt.Errorf("timed out waiting for node to be available")
		}
		time.Sleep(DepositPollInterval)
		nodesResp, err := sparkClient.QueryNodes(ctx, &pb.QueryNodesRequest{
			Source: &pb.QueryNodesRequest_NodeIds{NodeIds: &pb.TreeNodeIds{NodeIds: []string{node.Id}}},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to query nodes: %v", err)
		}
		if len(nodesResp.Nodes) != 1 {
			return nil, fmt.Errorf("expected 1 node, got %d", len(nodesResp.Nodes))
		}
		node = nodesResp.Nodes[node.Id]
	}
	return node, nil
}

// CreateNewTree creates a new Tree
func CreateNewTree(config *wallet.Config, faucet *Faucet, privKey *secp256k1.PrivateKey, amountSats int64) (*pb.TreeNode, error) {
	coin, err := faucet.Fund()
	if err != nil {
		return nil, fmt.Errorf("failed to fund faucet: %v", err)
	}

	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, &leafID, false)
	if err != nil {
		return nil, fmt.Errorf("failed to generate deposit address: %v", err)
	}

	depositTx, err := CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, amountSats)
	if err != nil {
		return nil, fmt.Errorf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize deposit tx: %v", err)
	}

	resp, err := wallet.CreateTreeRoot(ctx, config, privKey.Serialize(), depositResp.DepositAddress.VerifyingKey, depositTx, vout)
	if err != nil {
		return nil, fmt.Errorf("failed to create tree: %v", err)
	}
	if len(resp.Nodes) == 0 {
		return nil, fmt.Errorf("no nodes found after creating tree")
	}

	// Sign, broadcast, mine deposit tx
	signedExitTx, err := SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to sign deposit tx: %v", err)
	}

	client, err := NewRegtestClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create regtest client: %v", err)
	}
	_, err = client.SendRawTransaction(signedExitTx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to broadcast deposit tx: %v", err)
	}
	randomKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate random key: %v", err)
	}
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to mine deposit tx: %v", err)
	}

	// Wait until the deposited leaf is available
	sparkClient := pb.NewSparkServiceClient(conn)
	return WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
}

// CreateNewTree creates a new Tree
func CreateNewTreeWithLevels(config *wallet.Config, faucet *Faucet, privKey *secp256k1.PrivateKey, amountSats int64, levels uint32) (*wallet.DepositAddressTree, []*pb.TreeNode, error) {
	coin, err := faucet.Fund()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fund faucet: %v", err)
	}

	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)

	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	leafID := uuid.New().String()
	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes, &leafID, false)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate deposit address: %v", err)
	}

	depositTx, err := CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, amountSats)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize deposit tx: %v", err)
	}

	tree, err := wallet.GenerateDepositAddressesForTree(ctx, config, depositTx, nil, uint32(vout), privKey.Serialize(), levels)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create tree: %v", err)
	}

	treeNodes, err := wallet.CreateTree(ctx, config, depositTx, nil, uint32(vout), tree, true)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create tree: %v", err)
	}

	// Sign, broadcast, mine deposit tx
	signedExitTx, err := SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign deposit tx: %v", err)
	}

	client, err := NewRegtestClient()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create regtest client: %v", err)
	}
	_, err = client.SendRawTransaction(signedExitTx, true)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to broadcast deposit tx: %v", err)
	}
	randomKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate random key: %v", err)
	}
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate random address: %v", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to mine deposit tx: %v", err)
	}

	leafNode := treeNodes.Nodes[len(treeNodes.Nodes)-1]
	_, err = WaitForPendingDepositNode(ctx, pb.NewSparkServiceClient(conn), leafNode)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to wait for pending deposit node: %v", err)
	}

	return tree, treeNodes.Nodes, nil
}
