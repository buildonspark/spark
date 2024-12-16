package grpctest

import (
	"context"
	"log"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
)

func TestSplit(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}

	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("failed to create private key: %v", err)
	}
	rootNode, err := testutil.CreateNewTree(config, privKey)
	if err != nil {
		t.Fatalf("failed to create new tree: %v", err)
	}

	splitResp, err := wallet.SplitTreeNode(context.Background(), config, rootNode, 50_000, privKey.Serialize())
	if err != nil {
		t.Fatalf("failed to split tree node: %v", err)
	}

	log.Printf("split response: %v", splitResp)
}
