package testutil

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/wallet"
)

// CreateNewTree creates a new Tree
func CreateNewTree(config *wallet.Config, privKey *secp256k1.PrivateKey) (*pb.TreeNode, error) {
	// Setup Mock tx
	conn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	mockClient := pbmock.NewMockServiceClient(conn)
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	depositResp, err := wallet.GenerateDepositAddress(context.Background(), config, userPubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to generate deposit address: %v", err)
	}

	depositTx, err := CreateTestP2TRTransaction(depositResp.DepositAddress.Address, 100_000)
	if err != nil {
		return nil, fmt.Errorf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize deposit tx: %v", err)
	}
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode deposit tx hex: %v", err)
	}
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to deserilize deposit tx: %v", err)
	}

	log.Printf("deposit tx: %s", depositTxHex)
	mockClient.SetMockOnchainTx(context.Background(), &pbmock.SetMockOnchainTxRequest{
		Txid: depositTx.TxID(),
		Tx:   depositTxHex,
	})

	resp, err := wallet.CreateTreeRoot(context.Background(), config, privKey.Serialize(), depositResp.DepositAddress.VerifyingKey, depositTx, vout)
	if err != nil {
		return nil, fmt.Errorf("failed to create tree: %v", err)
	}
	return resp.Nodes[0], nil
}
