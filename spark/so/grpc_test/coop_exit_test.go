package grpctest

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
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
	sspConfig, err := testutil.TestWalletConfigWithIdentityKey(*sspPrivKey)
	if err != nil {
		t.Fatalf("failed to create ssp config: %v", err)
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

	connectorOutputs := make([]*wire.OutPoint, 0)
	for i := range connectorTx.TxOut[:len(connectorTx.TxOut)-1] {
		txHash := connectorTx.TxHash()
		connectorOutputs = append(connectorOutputs, wire.NewOutPoint(&txHash, uint32(i)))
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}

	senderTransfer, _, err := wallet.GetConnectorRefundSignatures(
		context.Background(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		exitTxHash.CloneBytes(),
		connectorOutputs,
		sspPubkey,
	)
	if err != nil {
		t.Fatalf("failed to get connector refund signatures: %v", err)
	}

	sspToken, err := wallet.AuthenticateWithServer(context.Background(), sspConfig)
	if err != nil {
		t.Fatalf("failed to authenticate receiver: %v", err)
	}
	sspCtx := wallet.ContextWithToken(context.Background(), sspToken)

	pendingTransfer, err := wallet.QueryPendingTransfers(sspCtx, sspConfig)
	if err != nil {
		t.Fatalf("failed to query pending transfers: %v", err)
	}
	if len(pendingTransfer.Transfers) != 1 {
		t.Fatalf("expected 1 pending transfer, got %d", len(pendingTransfer.Transfers))
	}
	receiverTransfer := pendingTransfer.Transfers[0]
	if receiverTransfer.Id != senderTransfer.Id {
		t.Fatalf("expected transfer id %s, got %s", senderTransfer.Id, receiverTransfer.Id)
	}

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), sspConfig, receiverTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if len(*leafPrivKeyMap) != 1 {
		t.Fatalf("Expected 1 leaf to transfer, got %d", len(*leafPrivKeyMap))
	}
	if !bytes.Equal((*leafPrivKeyMap)[rootNode.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}

	// Try to claim leaf before exit tx confirms -> should fail
	finalLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}
	claimingNode := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey.Serialize(),
		NewSigningPrivKey: finalLeafPrivKey.Serialize(),
	}
	leavesToClaim := [1]wallet.LeafKeyTweak{claimingNode}
	err = wallet.ClaimTransfer(
		sspCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	if err == nil {
		t.Fatalf("expected error claiming transfer before exit tx confirms")
	}

	// Broadcast/confirm exit tx
	var buf bytes.Buffer
	err = exitTx.Serialize(&buf)
	if err != nil {
		t.Fatalf("failed to serialize exit tx: %v", err)
	}
	for _, signingOperator := range config.SigningOperators {
		conn, err := common.NewGRPCConnection(signingOperator.Address)
		if err != nil {
			t.Fatalf("failed to connect to operator: %v", err)
		}
		defer conn.Close()
		mockClient := pbmock.NewMockServiceClient(conn)
		_, err = mockClient.SetMockOnchainTx(context.Background(), &pbmock.SetMockOnchainTxRequest{
			Txid: exitTx.TxID(),
			Tx:   hex.EncodeToString(buf.Bytes()),
		})
		if err != nil {
			t.Fatalf("failed to set mock onchain tx: %v", err)
		}
	}

	// Claim leaf
	err = wallet.ClaimTransfer(
		sspCtx,
		receiverTransfer,
		sspConfig,
		leavesToClaim[:],
	)
	if err != nil {
		t.Fatalf("failed to ClaimTransfer: %v", err)
	}
}
