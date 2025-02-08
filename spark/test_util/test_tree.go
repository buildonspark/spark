package testutil

import (
	"bytes"
	"context"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/wallet"
)

// CreateNewTree creates a new Tree
func CreateNewTree(config *wallet.Config, faucet *Faucet, privKey *secp256k1.PrivateKey, amountSats int64) (*pb.TreeNode, error) {
	coin, err := faucet.Fund()
	if err != nil {
		return nil, fmt.Errorf("failed to fund faucet: %v", err)
	}

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

	return resp.Nodes[0], nil
}
