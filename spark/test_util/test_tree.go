package testutil

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/chain"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/stretchr/testify/assert"
)

// CreateNewTree creates a new Tree
func CreateNewTree(t *testing.T, config *wallet.Config, privKey *secp256k1.PrivateKey, amountSats int64) (*pb.TreeNode, error) {
	client, err := chain.NewRegtestClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create regtest client: %v", err)
	}

	userOnChainKey, fundingTxOut, fundingOutPoint := FundFaucet(t, client)

	conn, err := common.NewGRPCConnection(config.CoodinatorAddress())
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

	depositResp, err := wallet.GenerateDepositAddress(ctx, config, userPubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to generate deposit address: %v", err)
	}

	depositTx, err := CreateTestDepositTransaction(fundingOutPoint, depositResp.DepositAddress.Address, amountSats)
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

	// Sign, broadcast, mine deposit tx
	signedExitTx := SignOnChainTx(t, depositTx, fundingTxOut, userOnChainKey)
	_, err = client.SendRawTransaction(signedExitTx, true)
	assert.NoError(t, err)
	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	assert.NoError(t, err)

	return resp.Nodes[0], nil
}
