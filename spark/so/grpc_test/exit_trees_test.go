package grpctest

import (
	"context"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExitSingleNodeTrees(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	if err != nil {
		t.Fatalf("failed to create wallet config: %v", err)
	}
	client, err := testutil.NewRegtestClient()
	require.NoError(t, err, "failed to create regtest client")

	roots := make([]*pb.TreeNode, 0)
	privKeys := make([]*secp256k1.PrivateKey, 0)
	treeAmountSats := 100_000
	for range 5 {
		priKey, err := secp256k1.GeneratePrivateKey()
		require.NoError(t, err, "failed to create node signing private key")
		root, err := testutil.CreateNewTree(config, faucet, priKey, int64(treeAmountSats))
		require.NoError(t, err, "failed to create new tree")
		roots = append(roots, root)
		privKeys = append(privKeys, priKey)
	}

	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	require.NoError(t, err, "failed to create random address")

	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()
	token, err := wallet.AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		t.Fatalf("failed to authenticate: %v", err)
	}
	ctx := wallet.ContextWithToken(context.Background(), token)
	tx, err := wallet.ExitSingleNodeTrees(ctx, config, client, roots, privKeys, randomAddress, int64(float64((treeAmountSats)*len(roots))*0.8))
	require.NoError(t, err, "failed to exit trees")

	_, err = client.SendRawTransaction(tx, true)
	assert.NoError(t, err, "failed to broadcast transaction")
}
