package grpctest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
)

func TestTransfer(t *testing.T) {
	// Sender initiates transfer
	senderConfig, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create sender wallet config: %v", err)
	}

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create node signing private key: %v", err)
	}
	rootNode, err := testutil.CreateNewTree(senderConfig, leafPrivKey)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new node signing private key: %v", err)
	}

	receiverPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create receiver private key: %v", err)
	}

	transferNode := wallet.LeafToTransfer{
		LeafID:            rootNode.Id,
		SigningPrivKey:    leafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}
	leavesToTransfer := [1]wallet.LeafToTransfer{transferNode}
	senderTransfer, err := wallet.SendTransfer(
		context.Background(),
		senderConfig,
		leavesToTransfer[:],
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("failed to transfer tree node: %v", err)
	}

	// Receiver queries pending transfer
	receiverConfig, err := testutil.TestWalletConfigWithIdentityKey(*receiverPrivKey)
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}
	pendingTransfer, err := wallet.QueryPendingTransfers(context.Background(), receiverConfig)
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

	leafPrivKeyMap, err := wallet.VerifyPendingTransfer(context.Background(), receiverConfig, receiverTransfer)
	if err != nil {
		t.Fatalf("unable to verify pending transfer: %v", err)
	}
	if !bytes.Equal((*leafPrivKeyMap)[rootNode.Id], newLeafPrivKey.Serialize()) {
		t.Fatalf("wrong leaf signing private key")
	}
}
