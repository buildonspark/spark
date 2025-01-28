package grpctest

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
)

func TestCoopExit(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	// Setup a user with some leaves
	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create node signing private key: %v", err)
	}
	rootNode, err := testutil.CreateNewTree(config, leafPrivKey)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	// Initiate SSP
	sspPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create ssp private key: %v", err)
	}
	sspPubkey := sspPrivKey.PubKey()
	sspIntermediateAddress, err := common.P2TRAddressFromPublicKey(sspPubkey.SerializeCompressed(), config.Network)
	if err != nil {
		t.Fatalf("failed to create ssp address: %v", err)
	}

	// Initiate exit - SSP is just another user, providing a service external to the SO
	amountSats := int64(100_000) // TODO: this should match the amount from the leaves
	withdrawPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create withdraw private key: %v", err)
	}
	withdrawPubKey := withdrawPrivKey.PubKey()
	withdrawAddress, err := common.P2TRAddressFromPublicKey(withdrawPubKey.SerializeCompressed(), config.Network)
	if err != nil {
		t.Fatalf("failed to create withdraw address: %v", err)
	}

	leafCount := 1                                                    // TODO: this should match the number of leaves
	dustAmountSats := 354                                             // TODO: this should match the proper dust
	intermediateAmountSats := int64((leafCount + 1) * dustAmountSats) // +1 for an output SSP can fee bump

	exitTx, err := testutil.CreateTestCoopExitTransaction(*withdrawAddress, amountSats, *sspIntermediateAddress, intermediateAmountSats)
	if err != nil {
		t.Fatalf("failed to create test transaction: %v", err)
	}

	exitTxHash := exitTx.TxHash()
	intermediateOutPoint := wire.NewOutPoint(&exitTxHash, 1)
	connectorP2trAddrs := make([]string, 0)
	for i := 0; i < leafCount+1; i++ {
		connectorPrivKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("failed to create connector private key: %v", err)
		}
		connectorPubKey := connectorPrivKey.PubKey()
		connectorAddress, err := common.P2TRAddressFromPublicKey(connectorPubKey.SerializeCompressed(), config.Network)
		if err != nil {
			t.Fatalf("failed to create connector address: %v", err)
		}
		connectorP2trAddrs = append(connectorP2trAddrs, *connectorAddress)
	}
	feeBumpAddr := connectorP2trAddrs[len(connectorP2trAddrs)-1]
	connectorP2trAddrs = connectorP2trAddrs[:len(connectorP2trAddrs)-1]
	connectorTx, err := testutil.CreateTestConnectorTransaction(intermediateOutPoint, intermediateAmountSats, connectorP2trAddrs, feeBumpAddr)
	if err != nil {
		t.Fatalf("failed to create test transaction: %v", err)
	}

	// Call wallet function to get signatures
	nodeTx, err := common.TxFromRawTxBytes(rootNode.GetNodeTx())
	if err != nil {
		t.Fatalf("failed to parse node tx: %v", err)
	}
	nodeTxHash := nodeTx.TxHash()
	leafOutPoint := wire.NewOutPoint(&nodeTxHash, 0)
	leaf := &wallet.Leaf{
		LeafID:        rootNode.Id,
		OutPoint:      leafOutPoint,
		SigningPubKey: leafPrivKey.PubKey(),
		AmountSats:    int64(rootNode.Value),
		TreeNode:      rootNode,
	}
	connectorOutputs := make([]*wire.OutPoint, 0)
	for i := range connectorTx.TxOut[:len(connectorTx.TxOut)-1] {
		txHash := connectorTx.TxHash()
		connectorOutputs = append(connectorOutputs, wire.NewOutPoint(&txHash, uint32(i)))
	}

	_, err = wallet.GetConnectorRefundSignatures(
		context.Background(),
		config,
		leafPrivKey,
		[]*wallet.Leaf{leaf},
		exitTxHash.CloneBytes(),
		connectorOutputs,
		sspPubkey,
	)
	if err != nil {
		t.Fatalf("failed to get connector refund signatures: %v", err)
	}

	// TODO: verify signatures, "broadcast" exit tx, claim leaves
}
