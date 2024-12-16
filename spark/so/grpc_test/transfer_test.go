package grpctest

import (
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
)

func TestSendTransfer(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create private key: %v", err)
	}
	rootNode, err := testutil.CreateNewTree(config, leafPrivKey)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create new private key: %v", err)
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
	_, err = wallet.SendTransfer(
		context.Background(),
		config,
		leavesToTransfer[:],
		receiverPrivKey.PubKey().SerializeCompressed(),
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("failed to transfer tree node: %v", err)
	}
}
