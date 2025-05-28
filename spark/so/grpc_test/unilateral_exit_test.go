package grpctest

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/require"
)

type SubmitPackageCmd struct {
	// An array of hex strings of raw transactions.
	RawTxns []string
}

type SubmitPackageResult struct {
	PackageMsg           string              `json:"package_msg"`
	TxResults            map[string]TxResult `json:"tx-results"`
	ReplacedTransactions []string            `json:"replaced-transactions"`
}

type TxResult struct {
	TxID  string `json:"txid"`
	Error string `json:"error,omitempty"`
	// Several fields omitted for brevity
}

func NewSubmitPackageCmd(rawTxns []string) *SubmitPackageCmd {
	return &SubmitPackageCmd{RawTxns: rawTxns}
}

func serializeTx(tx *wire.MsgTx) ([]byte, error) {
	var buf bytes.Buffer
	err := tx.Serialize(&buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// broadcast the refund tx, mine it, and assert it confirms in the block.
// If the tx has a timelock of X blocks, we'll assume the parent tx just confirmed,
// and mine X blocks before broadcasting the tx.
func broadcastAndConfirmTx(t *testing.T, client *rpcclient.Client, tx *wire.MsgTx) {
	randPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randPrivKey.PubKey().SerializeCompressed(), common.Regtest)
	require.NoError(t, err)

	// https://github.com/bitcoin/bips/blob/master/bip-0068.mediawiki
	// https://learnmeabitcoin.com/technical/transaction/input/sequence/
	timelockEnabled := tx.TxIn[0].Sequence <= 0xFFFFFFFE
	timelock := int64(tx.TxIn[0].Sequence & 0xFFFF)
	if timelockEnabled && timelock > 0 {
		_, err = client.GenerateToAddress(timelock, randomAddress, nil)
		require.NoError(t, err)
	}

	_, err = client.SendRawTransaction(tx, false)
	require.NoError(t, err)

	blockHashes, err := client.GenerateToAddress(1, randomAddress, nil)
	require.NoError(t, err)
	block, err := client.GetBlockVerbose(blockHashes[0])
	require.NoError(t, err)
	require.Contains(t, block.Tx, tx.TxID())
}

// Test we can unilateral exit a leaf node after depositing funds into
// a single leaf tree.
func TestUnilateralExitSingleLeaf(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	rootNode, err := testutil.CreateNewTree(config, faucet, leafPrivKey, 100_000)
	require.NoError(t, err)

	getCurrentTimelock := func(rootNode *pb.TreeNode) int64 {
		refundTx, err := common.TxFromRawTxBytes(rootNode.GetRefundTx())
		require.NoError(t, err)
		return int64(refundTx.TxIn[0].Sequence & 0xFFFF)
	}

	// Re-sign the leaf with decrement timelock so we don't need to mine so many blocks
	for getCurrentTimelock(rootNode) > spark.TimeLockInterval*2 {
		rootNode, err = wallet.RefreshTimelockRefundTx(context.Background(), config, rootNode, leafPrivKey)
		require.NoError(t, err)
	}

	client, err := testutil.NewRegtestClient()
	require.NoError(t, err)
	nodeTx, err := common.TxFromRawTxBytes(rootNode.GetNodeTx())
	require.NoError(t, err)
	broadcastAndConfirmTx(t, client, nodeTx)

	refundTx, err := common.TxFromRawTxBytes(rootNode.GetRefundTx())
	require.NoError(t, err)
	broadcastAndConfirmTx(t, client, refundTx)
	require.NoError(t, err)
}

// Test we can unilateral exit a leaf node of a tree with multiple leaves.
func TestUnilateralExitTreeLeaf(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	tree, nodes, err := testutil.CreateNewTreeWithLevels(config, faucet, leafPrivKey, 100_000, 1)
	require.NoError(t, err)
	require.Equal(t, 5, len(nodes))

	// These indices are hard-coded based on how we do tree construction
	rootNode := nodes[0]
	leafNode := nodes[len(nodes)-1]
	signingKeyBytes := tree.Children[1].SigningPrivateKey
	signingKey := secp256k1.PrivKeyFromBytes(signingKeyBytes)
	parentNode := nodes[len(nodes)-3]
	require.Equal(t, parentNode.Id, *leafNode.ParentNodeId)

	// Decrement our timelocks so we don't need to mine so many blocks
	getTimelock := func(txBytes []byte) int64 {
		tx, err := common.TxFromRawTxBytes(txBytes)
		require.NoError(t, err)
		return int64(tx.TxIn[0].Sequence & 0xFFFF)
	}

	for getTimelock(leafNode.NodeTx) > spark.TimeLockInterval*2 {
		nodes, err = wallet.RefreshTimelockNodes(context.Background(), config, []*pb.TreeNode{leafNode}, parentNode, signingKey)
		leafNode = nodes[0]
		require.NoError(t, err)
	}

	for getTimelock(leafNode.RefundTx) > spark.TimeLockInterval*2 {
		leafNode, err = wallet.RefreshTimelockRefundTx(context.Background(), config, leafNode, signingKey)
		require.NoError(t, err)
	}

	client, err := testutil.NewRegtestClient()
	require.NoError(t, err)

	rootNodeTx, err := common.TxFromRawTxBytes(rootNode.GetNodeTx())
	require.NoError(t, err)
	broadcastAndConfirmTx(t, client, rootNodeTx)
	require.NoError(t, err)

	parentNodeTx, err := common.TxFromRawTxBytes(parentNode.GetNodeTx())
	require.NoError(t, err)
	broadcastAndConfirmTx(t, client, parentNodeTx)

	nodeTx, err := common.TxFromRawTxBytes(leafNode.GetNodeTx())
	require.NoError(t, err)
	broadcastAndConfirmTx(t, client, nodeTx)

	refundTx, err := common.TxFromRawTxBytes(leafNode.GetRefundTx())
	require.NoError(t, err)
	broadcastAndConfirmTx(t, client, refundTx)
}
