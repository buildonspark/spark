package grpctest

import (
	"bytes"
	"context"
	"log"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
)

func TestSplitAndAggregate(t *testing.T) {
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

	splitResp, nodeSigningKeys, err := wallet.SplitTreeNode(context.Background(), config, rootNode, 50_000, privKey.Serialize())
	if err != nil {
		t.Fatalf("failed to split tree node: %v", err)
	}

	log.Printf("split response: %v", splitResp)

	sum := nodeSigningKeys[0]
	for i, key := range nodeSigningKeys {
		if i == 0 {
			continue
		}
		sum, err = common.AddPrivateKeys(sum, key)
		if err != nil {
			t.Fatalf("failed to add private keys: %v", err)
		}
	}

	if !bytes.Equal(sum, privKey.Serialize()) {
		t.Fatalf("sum of node signing keys is not equal to parent private key")
	}

	splitNodes := splitResp.Nodes

	aggregateResp, err := wallet.AggregateTreeNodes(context.Background(), config, splitNodes, rootNode, sum)
	if err != nil {
		t.Fatalf("failed to aggregate nodes: %v", err)
	}

	log.Printf("aggregate response: %v", aggregateResp)
}
