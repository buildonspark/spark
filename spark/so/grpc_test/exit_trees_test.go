package grpctest

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

func TestExitSingleNodeTrees(t *testing.T) {
	config := sparktesting.TestWalletConfig(t)
	client := sparktesting.GetBitcoinClient()

	var roots []*pb.TreeNode
	var privKeys []keys.Private
	treeAmountSats := 100_000
	for range 5 {
		priKey, err := keys.GeneratePrivateKey()
		require.NoError(t, err, "failed to create node signing private key")
		root, err := sparktesting.CreateNewTree(config, faucet, priKey, int64(treeAmountSats))
		require.NoError(t, err, "failed to create new tree")
		roots = append(roots, root)
		privKeys = append(privKeys, priKey)
	}

	randomKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	require.NoError(t, err, "failed to create random address")

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err, "failed to connect to operator")
	defer conn.Close()
	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err, "failed to authenticate")
	ctx := wallet.ContextWithToken(t.Context(), token)
	tx, err := wallet.ExitSingleNodeTrees(ctx, config, client, roots, privKeys, randomAddress, int64(float64((treeAmountSats)*len(roots))*0.8))
	require.NoError(t, err, "failed to exit trees")

	_, err = client.SendRawTransaction(tx, true)
	require.NoError(t, err, "failed to broadcast transaction")
}
