package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func createValidTestTransactionBytesWithSequence(t *testing.T, sequence uint32) []byte {
	tx := wire.NewMsgTx(2)
	prevHash, _ := chainhash.NewHashFromStr(strings.Repeat("0", 64))

	// Create TxIn with specific sequence value
	txIn := wire.NewTxIn(&wire.OutPoint{Hash: *prevHash, Index: 0}, nil, nil)
	txIn.Sequence = sequence
	tx.AddTxIn(txIn)

	tx.AddTxOut(wire.NewTxOut(100000, []byte("test-pkscript")))

	var buf bytes.Buffer
	err := tx.Serialize(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func createTestUserSignedTxSigningJob(t *testing.T, rng io.Reader, leafNode *ent.TreeNode, rawTx []byte) *pb.UserSignedTxSigningJob {
	return &pb.UserSignedTxSigningJob{
		LeafId:           leafNode.ID.String(),
		SigningPublicKey: leafNode.OwnerSigningPubkey.Serialize(),
		RawTx:            rawTx,
		SigningNonceCommitment: &pbcommon.SigningCommitment{
			Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
		},
		UserSignature: []byte("test_user_signature"),
		SigningCommitments: &pb.SigningCommitments{
			SigningCommitments: map[string]*pbcommon.SigningCommitment{
				"test_operator": {
					Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
					Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
				},
			},
		},
	}
}

func createTestRenewNodeTimelockSigningJob(t *testing.T, rng io.Reader, leafNode *ent.TreeNode, updateBits uint32) *pb.RenewNodeTimelockSigningJob {
	// Create transaction data with appropriate Spark sequence values for each type
	// Based on the proto comments and Spark constants:
	// - Split node tx should have spark.ZeroSequence (0x40000000)
	// - Split node direct tx should have just spark.DirectTimelockOffset (0x32)
	// - Updated node tx should have spark.InitialSequence() (0x400007D0)
	// - Updated refund tx should have spark.InitialSequence() (0x400007D0)
	// - Other direct transactions add DirectTimelockOffset to InitialSequence (0x40000802)
	splitNodeTx := createValidTestTransactionBytesWithSequence(t, spark.ZeroSequence|updateBits)
	splitNodeDirectTx := createValidTestTransactionBytesWithSequence(t, spark.DirectTimelockOffset|updateBits)
	nodeTx := createValidTestTransactionBytesWithSequence(t, spark.InitialSequence()|updateBits)
	refundTx := createValidTestTransactionBytesWithSequence(t, spark.InitialSequence()|updateBits)
	directTx := createValidTestTransactionBytesWithSequence(t, (spark.InitialSequence()+spark.DirectTimelockOffset)|updateBits)

	return &pb.RenewNodeTimelockSigningJob{
		SplitNodeTxSigningJob:            createTestUserSignedTxSigningJob(t, rng, leafNode, splitNodeTx),
		SplitNodeDirectTxSigningJob:      createTestUserSignedTxSigningJob(t, rng, leafNode, splitNodeDirectTx),
		NodeTxSigningJob:                 createTestUserSignedTxSigningJob(t, rng, leafNode, nodeTx),
		RefundTxSigningJob:               createTestUserSignedTxSigningJob(t, rng, leafNode, refundTx),
		DirectNodeTxSigningJob:           createTestUserSignedTxSigningJob(t, rng, leafNode, directTx),
		DirectRefundTxSigningJob:         createTestUserSignedTxSigningJob(t, rng, leafNode, directTx),
		DirectFromCpfpRefundTxSigningJob: createTestUserSignedTxSigningJob(t, rng, leafNode, directTx),
	}
}

func createTestRenewRefundTimelockSigningJob(t *testing.T, rng io.Reader, leafNode *ent.TreeNode, updateBits uint32) *pb.RenewRefundTimelockSigningJob {
	nodeTx := createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock-spark.TimeLockInterval)|updateBits)
	refundTx := createValidTestTransactionBytesWithSequence(t, spark.InitialTimeLock|updateBits)
	directNodeTx := createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock-spark.TimeLockInterval+spark.DirectTimelockOffset)|updateBits)
	directRefundTx := createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock+spark.DirectTimelockOffset)|updateBits)
	directFromCpfpRefundTx := createValidTestTransactionBytesWithSequence(t, spark.InitialTimeLock+spark.DirectTimelockOffset|updateBits)

	return &pb.RenewRefundTimelockSigningJob{
		NodeTxSigningJob:                 createTestUserSignedTxSigningJob(t, rng, leafNode, nodeTx),
		RefundTxSigningJob:               createTestUserSignedTxSigningJob(t, rng, leafNode, refundTx),
		DirectNodeTxSigningJob:           createTestUserSignedTxSigningJob(t, rng, leafNode, directNodeTx),
		DirectRefundTxSigningJob:         createTestUserSignedTxSigningJob(t, rng, leafNode, directRefundTx),
		DirectFromCpfpRefundTxSigningJob: createTestUserSignedTxSigningJob(t, rng, leafNode, directFromCpfpRefundTx),
	}
}

func createTestRenewNodeZeroTimelockSigningJob(t *testing.T, rng io.Reader, leafNode *ent.TreeNode, updateBits uint32) *pb.RenewNodeZeroTimelockSigningJob {
	nodeTx := createValidTestTransactionBytesWithSequence(t, spark.ZeroTimelock|updateBits)
	refundTx := createValidTestTransactionBytesWithSequence(t, spark.InitialTimeLock|updateBits)
	directTx := createValidTestTransactionBytesWithSequence(t, spark.DirectTimelockOffset|updateBits)
	directFromCpfpRefundTx := createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock+spark.DirectTimelockOffset)|updateBits)

	return &pb.RenewNodeZeroTimelockSigningJob{
		NodeTxSigningJob:                 createTestUserSignedTxSigningJob(t, rng, leafNode, nodeTx),
		RefundTxSigningJob:               createTestUserSignedTxSigningJob(t, rng, leafNode, refundTx),
		DirectNodeTxSigningJob:           createTestUserSignedTxSigningJob(t, rng, leafNode, directTx),
		DirectFromCpfpRefundTxSigningJob: createTestUserSignedTxSigningJob(t, rng, leafNode, directFromCpfpRefundTx),
	}
}

func createTestRenewSigningKeyshare(t *testing.T, ctx context.Context, rng io.Reader) *ent.SigningKeyshare {
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	pubSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	signingKeyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{"operator1": pubSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	return signingKeyshare
}

func createTestRenewTree(t *testing.T, ctx context.Context, ownerIdentityPubKey keys.Public) *ent.Tree {
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Simply setting random bytes for unique tree
	baseTxid := st.NewRandomTxIDForTesting(t)
	tree, err := tx.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetBaseTxid(baseTxid).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return tree
}

func createTestRenewTreeNode(t *testing.T, ctx context.Context, rng io.Reader, dbClient *ent.Client, tree *ent.Tree, keyshare *ent.SigningKeyshare, parent *ent.TreeNode, updateBits uint32) *ent.TreeNode {
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create transactions with the appropriate keys
	nodeTxMsg, err := sparktesting.CreateTestP2TRTransactionWithSequence(t, verifyingPubKey, spark.InitialTimeLock|updateBits, int64(100000))
	require.NoError(t, err)
	nodeTx, err := common.SerializeTx(nodeTxMsg)
	require.NoError(t, err)

	ownerSigningAddr, err := common.P2TRAddressFromPublicKey(ownerSigningPubKey, btcnetwork.Regtest)
	require.NoError(t, err)
	refundTxMsg, err := sparktesting.CreateTestP2TRTransaction(ownerSigningAddr, 100000)
	require.NoError(t, err)
	refundTx, err := common.SerializeTx(refundTxMsg)
	require.NoError(t, err)

	nodeCreate := dbClient.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(keyshare).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(ownerPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetRawTx(nodeTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(nodeTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(0)

	if parent != nil {
		nodeCreate = nodeCreate.SetParent(parent)
	}

	leaf, err := nodeCreate.Save(ctx)
	require.NoError(t, err)
	return leaf
}

func TestRenewLeafRejectsSessionMismatchBeforeConsensus(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	leaf := createTestNodeForFlowHandler(t, ctx, st.TreeNodeStatusAvailable)
	sessionIdentity := keys.GeneratePrivateKey().Public()
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(sessionIdentity.Serialize()), 9999999999)

	cfg := sparktesting.TestConfig(t)
	cfg.AuthzEnforced = true
	handler := NewRenewLeafHandler(cfg)

	resp, err := handler.RenewLeaf(ctx, &pb.RenewLeafRequest{
		LeafId: leaf.ID.String(),
	})

	require.Nil(t, resp)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestRenewLeafRejectsMalformedOrIncompleteRequests(t *testing.T) {
	handler := NewRenewLeafHandler(sparktesting.TestConfig(t))

	t.Run("malformed leaf id", func(t *testing.T) {
		resp, err := handler.RenewLeaf(t.Context(), &pb.RenewLeafRequest{
			LeafId: "not-a-uuid",
		})
		require.Nil(t, resp)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "failed to parse leaf id")
	})

	t.Run("missing signing jobs", func(t *testing.T) {
		ctx, _ := db.ConnectToTestPostgres(t)
		leaf := createTestNodeForFlowHandler(t, ctx, st.TreeNodeStatusAvailable)

		resp, err := handler.RenewLeaf(ctx, &pb.RenewLeafRequest{
			LeafId: leaf.ID.String(),
		})
		require.Nil(t, resp)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "request must specify a signing job")
	})
}

func TestConstructRenewNodeTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tests := []struct {
		name       string
		updateBits uint32
	}{
		{
			name:       "normal case",
			updateBits: 0,
		},
		{
			name:       "30th bit set",
			updateBits: (1 << 30),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test data
			ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
			tree := createTestRenewTree(t, ctx, ownerPubKey)

			// Create parent node
			parentNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, tt.updateBits)

			// Create leaf node with parent
			leafNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, parentNode, tt.updateBits)

			// Get expected pk scripts
			expectedVerifyingPkScript, err := common.P2TRScriptFromPubKey(leafNode.VerifyingPubkey)
			require.NoError(t, err)
			expectedOwnerSigningPkScript, err := common.P2TRScriptFromPubKey(leafNode.OwnerSigningPubkey)
			require.NoError(t, err)

			// Create a test signing job with the specific updateBits
			signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, tt.updateBits)

			// Test the function
			renewTxs, err := constructRenewNodeTransactions(leafNode, parentNode, signingJob)
			require.NoError(t, err)

			// Verify split node transaction
			assert.NotNil(t, renewTxs.SplitNodeTx)
			assert.Len(t, renewTxs.SplitNodeTx.TxIn, 1)
			assert.Len(t, renewTxs.SplitNodeTx.TxOut, 2) // main output + ephemeral anchor
			assert.Equal(t, spark.ZeroSequence|tt.updateBits, renewTxs.SplitNodeTx.TxIn[0].Sequence)
			// Verify main output pk script
			assert.Equal(t, expectedVerifyingPkScript, renewTxs.SplitNodeTx.TxOut[0].PkScript)
			// Verify second output is ephemeral anchor
			assert.Equal(t, int64(0), renewTxs.SplitNodeTx.TxOut[1].Value)
			assert.Equal(t, common.EphemeralAnchorOutput().PkScript, renewTxs.SplitNodeTx.TxOut[1].PkScript)

			// Parse parent tx to check values
			parentTx, err := common.TxFromRawTxBytes(parentNode.RawTx)
			require.NoError(t, err)
			parentAmount := parentTx.TxOut[0].Value

			// Split node should use parent tx hash and parent amount
			assert.Equal(t, parentTx.TxHash(), renewTxs.SplitNodeTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, uint32(0), renewTxs.SplitNodeTx.TxIn[0].PreviousOutPoint.Index)
			assert.Equal(t, parentAmount, renewTxs.SplitNodeTx.TxOut[0].Value)

			// Verify extended node transaction
			assert.NotNil(t, renewTxs.NodeTx)
			assert.Len(t, renewTxs.NodeTx.TxIn, 1)
			assert.Len(t, renewTxs.NodeTx.TxOut, 2) // main output + ephemeral anchor
			assert.Equal(t, spark.InitialSequence()|tt.updateBits, renewTxs.NodeTx.TxIn[0].Sequence)
			assert.Equal(t, renewTxs.SplitNodeTx.TxHash(), renewTxs.NodeTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, parentAmount, renewTxs.NodeTx.TxOut[0].Value)
			// Verify main output pk script
			assert.Equal(t, expectedVerifyingPkScript, renewTxs.NodeTx.TxOut[0].PkScript)
			// Verify second output is ephemeral anchor
			assert.Equal(t, int64(0), renewTxs.NodeTx.TxOut[1].Value)
			assert.Equal(t, common.EphemeralAnchorOutput().PkScript, renewTxs.NodeTx.TxOut[1].PkScript)

			// Verify refund transaction
			assert.NotNil(t, renewTxs.RefundTx)
			assert.Len(t, renewTxs.RefundTx.TxIn, 1)
			assert.Len(t, renewTxs.RefundTx.TxOut, 2) // main output + ephemeral anchor
			assert.Equal(t, spark.InitialSequence()|tt.updateBits, renewTxs.RefundTx.TxIn[0].Sequence)
			assert.Equal(t, renewTxs.NodeTx.TxHash(), renewTxs.RefundTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, parentAmount, renewTxs.RefundTx.TxOut[0].Value)
			// Verify main output pk script
			assert.Equal(t, expectedOwnerSigningPkScript, renewTxs.RefundTx.TxOut[0].PkScript)
			// Verify second output is ephemeral anchor
			assert.Equal(t, int64(0), renewTxs.RefundTx.TxOut[1].Value)
			assert.Equal(t, common.EphemeralAnchorOutput().PkScript, renewTxs.RefundTx.TxOut[1].PkScript)

			// Verify direct split node transaction
			assert.NotNil(t, renewTxs.DirectSplitNodeTx)
			assert.Len(t, renewTxs.DirectSplitNodeTx.TxIn, 1)
			assert.Len(t, renewTxs.DirectSplitNodeTx.TxOut, 1)
			assert.Equal(t, spark.DirectTimelockOffset|tt.updateBits, renewTxs.DirectSplitNodeTx.TxIn[0].Sequence)
			assert.Equal(t, parentTx.TxHash(), renewTxs.DirectSplitNodeTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(parentAmount), renewTxs.DirectSplitNodeTx.TxOut[0].Value)
			assert.Equal(t, expectedVerifyingPkScript, renewTxs.DirectSplitNodeTx.TxOut[0].PkScript)

			// Verify direct node transaction
			assert.NotNil(t, renewTxs.DirectNodeTx)
			assert.Len(t, renewTxs.DirectNodeTx.TxIn, 1)
			assert.Len(t, renewTxs.DirectNodeTx.TxOut, 1)
			assert.Equal(t, (spark.InitialSequence()+spark.DirectTimelockOffset)|tt.updateBits, renewTxs.DirectNodeTx.TxIn[0].Sequence)
			assert.Equal(t, renewTxs.SplitNodeTx.TxHash(), renewTxs.DirectNodeTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(parentAmount), renewTxs.DirectNodeTx.TxOut[0].Value)
			assert.Equal(t, expectedVerifyingPkScript, renewTxs.DirectNodeTx.TxOut[0].PkScript)

			// Verify direct refund transaction
			assert.NotNil(t, renewTxs.DirectRefundTx)
			assert.Len(t, renewTxs.DirectRefundTx.TxIn, 1)
			assert.Len(t, renewTxs.DirectRefundTx.TxOut, 1)
			assert.Equal(t, (spark.InitialSequence()+spark.DirectTimelockOffset)|tt.updateBits, renewTxs.DirectRefundTx.TxIn[0].Sequence)
			assert.Equal(t, renewTxs.DirectNodeTx.TxHash(), renewTxs.DirectRefundTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(common.MaybeApplyFee(parentAmount)), renewTxs.DirectRefundTx.TxOut[0].Value)
			assert.Equal(t, expectedOwnerSigningPkScript, renewTxs.DirectRefundTx.TxOut[0].PkScript)

			// Verify direct from CPFP refund transaction
			assert.NotNil(t, renewTxs.DirectFromCpfpRefundTx)
			assert.Len(t, renewTxs.DirectFromCpfpRefundTx.TxIn, 1)
			assert.Len(t, renewTxs.DirectFromCpfpRefundTx.TxOut, 1)
			assert.Equal(t, (spark.InitialSequence()+spark.DirectTimelockOffset)|tt.updateBits, renewTxs.DirectFromCpfpRefundTx.TxIn[0].Sequence)
			assert.Equal(t, renewTxs.NodeTx.TxHash(), renewTxs.DirectFromCpfpRefundTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(parentAmount), renewTxs.DirectFromCpfpRefundTx.TxOut[0].Value)
			assert.Equal(t, expectedOwnerSigningPkScript, renewTxs.DirectFromCpfpRefundTx.TxOut[0].PkScript)
		})
	}
}

func setRenewParentRawTxOutputs(t *testing.T, parent *ent.TreeNode, outputValues ...int64) *wire.MsgTx {
	t.Helper()

	parentTx, err := common.TxFromRawTxBytes(parent.RawTx)
	require.NoError(t, err)
	parentTx.TxOut = nil
	for _, value := range outputValues {
		pkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
		require.NoError(t, err)
		parentTx.AddTxOut(wire.NewTxOut(value, pkScript))
	}
	rawParentTx, err := common.SerializeTx(parentTx)
	require.NoError(t, err)
	parent.RawTx = rawParentTx
	return parentTx
}

func serializeRenewParentVoutTestTx(t *testing.T, tx *wire.MsgTx) []byte {
	t.Helper()

	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return raw
}

func TestConstructRenewTransactionsUseLeafParentVout(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{42})
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerPubKey)
	parentNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, 0)
	leafNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, parentNode, 0)
	leafNode.Vout = 1
	parentTx := setRenewParentRawTxOutputs(t, parentNode, 111_000, 222_000)
	parentAmount := parentTx.TxOut[leafNode.Vout].Value

	nodeSigningJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
	nodeRenewTxs, err := constructRenewNodeTransactions(leafNode, parentNode, nodeSigningJob)
	require.NoError(t, err)

	require.Equal(t, uint32(leafNode.Vout), nodeRenewTxs.SplitNodeTx.TxIn[0].PreviousOutPoint.Index)
	require.Equal(t, uint32(leafNode.Vout), nodeRenewTxs.DirectSplitNodeTx.TxIn[0].PreviousOutPoint.Index)
	require.Equal(t, parentAmount, nodeRenewTxs.SplitNodeTx.TxOut[0].Value)
	require.Equal(t, parentAmount, nodeRenewTxs.NodeTx.TxOut[0].Value)
	require.Equal(t, parentAmount, nodeRenewTxs.RefundTx.TxOut[0].Value)
	require.Equal(t, common.MaybeApplyFee(parentAmount), nodeRenewTxs.DirectSplitNodeTx.TxOut[0].Value)
	require.Equal(t, common.MaybeApplyFee(parentAmount), nodeRenewTxs.DirectNodeTx.TxOut[0].Value)
	require.Equal(t, common.MaybeApplyFee(parentAmount), nodeRenewTxs.DirectFromCpfpRefundTx.TxOut[0].Value)

	refundSigningJob := createTestRenewRefundTimelockSigningJob(t, rng, leafNode, 0)
	refundRenewTxs, err := constructRenewRefundTransactions(leafNode, parentNode, refundSigningJob)
	require.NoError(t, err)

	require.Equal(t, uint32(leafNode.Vout), refundRenewTxs.NodeTx.TxIn[0].PreviousOutPoint.Index)
	require.Equal(t, uint32(leafNode.Vout), refundRenewTxs.DirectNodeTx.TxIn[0].PreviousOutPoint.Index)
	require.Equal(t, parentAmount, refundRenewTxs.NodeTx.TxOut[0].Value)
	require.Equal(t, parentAmount, refundRenewTxs.RefundTx.TxOut[0].Value)
	require.Equal(t, common.MaybeApplyFee(parentAmount), refundRenewTxs.DirectNodeTx.TxOut[0].Value)
	require.Equal(t, common.MaybeApplyFee(parentAmount), refundRenewTxs.DirectFromCpfpRefundTx.TxOut[0].Value)
}

func TestConstructRenewTransactionsRejectOutOfRangeLeafParentVout(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{43})
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerPubKey)
	parentNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, 0)
	leafNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, parentNode, 0)
	leafNode.Vout = 2
	setRenewParentRawTxOutputs(t, parentNode, 111_000, 222_000)

	nodeSigningJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
	_, err = constructRenewNodeTransactions(leafNode, parentNode, nodeSigningJob)
	require.ErrorContains(t, err, "parent node transaction output 2 out of range")

	refundSigningJob := createTestRenewRefundTimelockSigningJob(t, rng, leafNode, 0)
	_, err = constructRenewRefundTransactions(leafNode, parentNode, refundSigningJob)
	require.ErrorContains(t, err, "parent node transaction output 2 out of range")
}

func createRenewParentVoutValidationFixture(t *testing.T, leafNodeSequence uint32, leafRefundSequence uint32) (context.Context, io.Reader, *ent.TreeNode, *ent.TreeNode) {
	t.Helper()

	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{71})
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerIdentityPubKey)
	parent := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, 0)
	leaf := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, parent, 0)
	leaf, err = leaf.Update().
		SetRawTx(createValidTestTransactionBytesWithSequence(t, leafNodeSequence)).
		SetRawRefundTx(createValidTestTransactionBytesWithSequence(t, leafRefundSequence)).
		Save(ctx)
	require.NoError(t, err)

	return ctx, rng, leaf, parent
}

func setValidRenewParentVoutNodeTimelockRawTxs(t *testing.T, signingJob *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions) {
	t.Helper()

	signingJob.SplitNodeTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.SplitNodeTx)
	signingJob.NodeTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.NodeTx)
	signingJob.RefundTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.RefundTx)
	signingJob.SplitNodeDirectTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.DirectSplitNodeTx)
	signingJob.DirectNodeTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.DirectNodeTx)
	signingJob.DirectRefundTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.DirectRefundTx)
	signingJob.DirectFromCpfpRefundTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.DirectFromCpfpRefundTx)
}

func setValidRenewParentVoutRefundTimelockRawTxs(t *testing.T, signingJob *pb.RenewRefundTimelockSigningJob, txs *RenewRefundTransactions) {
	t.Helper()

	signingJob.NodeTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.NodeTx)
	signingJob.RefundTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.RefundTx)
	signingJob.DirectNodeTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.DirectNodeTx)
	signingJob.DirectRefundTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.DirectRefundTx)
	signingJob.DirectFromCpfpRefundTxSigningJob.RawTx = serializeRenewParentVoutTestTx(t, txs.DirectFromCpfpRefundTx)
}

func TestValidateAndConstructRenewNodeTimelockSigningEntriesUseLeafParentVout(t *testing.T) {
	ctx, rng, leaf, parent := createRenewParentVoutValidationFixture(t, spark.RenewTimelockThreshold|spark.ZeroSequence, spark.RenewTimelockThreshold|spark.ZeroSequence)
	leaf.Vout = 1
	parentTx := setRenewParentRawTxOutputs(t, parent, 111_000, 222_000)
	parent, err := parent.Update().SetRawTx(parent.RawTx).Save(ctx)
	require.NoError(t, err)

	signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leaf, 0)
	renewTxs, err := constructRenewNodeTransactions(leaf, parent, signingJob)
	require.NoError(t, err)
	setValidRenewParentVoutNodeTimelockRawTxs(t, signingJob, renewTxs)

	_, _, entries, err := validateAndConstructNodeTimelock(ctx, leaf, signingJob)
	require.NoError(t, err)
	require.Len(t, entries, 7)

	require.Equal(t, parentTx.TxOut[1], entries[2].PrevOut)
	require.Equal(t, parentTx.TxOut[1], entries[3].PrevOut)
	require.NotEqual(t, parentTx.TxOut[0].Value, entries[2].PrevOut.Value)
	require.NotEqual(t, parentTx.TxOut[0].Value, entries[3].PrevOut.Value)
}

func TestValidateAndConstructRenewRefundTimelockSigningEntriesUseLeafParentVout(t *testing.T) {
	ctx, rng, leaf, parent := createRenewParentVoutValidationFixture(t, spark.InitialTimeLock|spark.ZeroSequence, spark.TimeLockInterval|spark.ZeroSequence)
	leaf.Vout = 1
	parentTx := setRenewParentRawTxOutputs(t, parent, 111_000, 222_000)
	parent, err := parent.Update().SetRawTx(parent.RawTx).Save(ctx)
	require.NoError(t, err)

	signingJob := createTestRenewRefundTimelockSigningJob(t, rng, leaf, 0)
	renewTxs, err := constructRenewRefundTransactions(leaf, parent, signingJob)
	require.NoError(t, err)
	setValidRenewParentVoutRefundTimelockRawTxs(t, signingJob, renewTxs)

	_, _, entries, err := validateAndConstructRefundTimelock(ctx, leaf, signingJob)
	require.NoError(t, err)
	require.Len(t, entries, 5)

	require.Equal(t, parentTx.TxOut[1], entries[0].PrevOut)
	require.Equal(t, parentTx.TxOut[1], entries[2].PrevOut)
	require.NotEqual(t, parentTx.TxOut[0].Value, entries[0].PrevOut.Value)
	require.NotEqual(t, parentTx.TxOut[0].Value, entries[2].PrevOut.Value)
}

func TestValidateAndConstructRenewSigningJobsRejectMissingRequiredJobs(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{19})
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerIdentityPubKey)
	parent := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, 0)

	const sequenceFlag = 1 << 30
	makeLeaf := func(nodeTimelock uint32, refundTimelock uint32) *ent.TreeNode {
		t.Helper()
		leaf := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, parent, 0)
		leaf, err = leaf.Update().
			SetRawTx(createValidTestTransactionBytesWithSequence(t, nodeTimelock|sequenceFlag)).
			SetRawRefundTx(createValidTestTransactionBytesWithSequence(t, refundTimelock|sequenceFlag)).
			Save(ctx)
		require.NoError(t, err)
		return leaf
	}

	nodeLeaf := makeLeaf(100, 100)
	_, _, _, err = validateAndConstructNodeTimelock(ctx, nodeLeaf, nil)
	require.ErrorContains(t, err, "renew node timelock signing job is required")
	_, _, _, err = validateAndConstructNodeTimelock(ctx, nodeLeaf, &pb.RenewNodeTimelockSigningJob{})
	require.ErrorContains(t, err, "split node tx signing job is required")

	refundLeaf := makeLeaf(200, 100)
	_, _, _, err = validateAndConstructRefundTimelock(ctx, refundLeaf, nil)
	require.ErrorContains(t, err, "renew refund timelock signing job is required")
	_, _, _, err = validateAndConstructRefundTimelock(ctx, refundLeaf, &pb.RenewRefundTimelockSigningJob{})
	require.ErrorContains(t, err, "node tx signing job is required")

	zeroLeaf := makeLeaf(0, 100)
	_, _, err = validateAndConstructNodeZeroTimelock(zeroLeaf, nil)
	require.ErrorContains(t, err, "renew node zero timelock signing job is required")
	_, _, err = validateAndConstructNodeZeroTimelock(zeroLeaf, &pb.RenewNodeZeroTimelockSigningJob{})
	require.ErrorContains(t, err, "node tx signing job is required")
}

func TestRenewLeafRejectsNilRequest(t *testing.T) {
	handler := NewRenewLeafHandler(nil)

	require.NotPanics(t, func() {
		resp, err := handler.RenewLeaf(t.Context(), nil)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "request is required")
	})
}

func TestConstructRenewTransactionsRejectUnsupportedSequenceHighBits(t *testing.T) {
	const unsupportedHighBit = uint32(1 << 16)

	newFixture := func(t *testing.T) (context.Context, io.Reader, *ent.TreeNode, *ent.TreeNode) {
		t.Helper()
		ctx, _ := db.NewTestSQLiteContext(t)
		rng := rand.NewChaCha8([32]byte{31})
		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
		tree := createTestRenewTree(t, ctx, ownerIdentityPubKey)
		parentNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, 0)
		leafNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, parentNode, 0)
		return ctx, rng, leafNode, parentNode
	}

	tests := []struct {
		name string
		run  func(*testing.T, context.Context, io.Reader, *ent.TreeNode, *ent.TreeNode) error
	}{
		{
			name: "renew node split node tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.SplitNodeTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, spark.ZeroSequence|unsupportedHighBit)
				_, err := constructRenewNodeTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew node split direct tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.SplitNodeDirectTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, spark.DirectTimelockOffset|unsupportedHighBit)
				_, err := constructRenewNodeTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew node node tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.NodeTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, spark.InitialSequence()|unsupportedHighBit)
				_, err := constructRenewNodeTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew node refund tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.RefundTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, spark.InitialSequence()|unsupportedHighBit)
				_, err := constructRenewNodeTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew node direct node tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.DirectNodeTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, (spark.InitialSequence()+spark.DirectTimelockOffset)|unsupportedHighBit)
				_, err := constructRenewNodeTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew node direct refund tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.DirectRefundTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, (spark.InitialSequence()+spark.DirectTimelockOffset)|unsupportedHighBit)
				_, err := constructRenewNodeTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew node direct from cpfp refund tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.DirectFromCpfpRefundTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, (spark.InitialSequence()+spark.DirectTimelockOffset)|unsupportedHighBit)
				_, err := constructRenewNodeTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew refund node tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewRefundTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.NodeTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock-spark.TimeLockInterval)|unsupportedHighBit)
				_, err := constructRenewRefundTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew refund refund tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewRefundTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.RefundTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, spark.InitialTimeLock|unsupportedHighBit)
				_, err := constructRenewRefundTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew refund direct node tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewRefundTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.DirectNodeTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock-spark.TimeLockInterval+spark.DirectTimelockOffset)|unsupportedHighBit)
				_, err := constructRenewRefundTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew refund direct refund tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewRefundTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.DirectRefundTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock+spark.DirectTimelockOffset)|unsupportedHighBit)
				_, err := constructRenewRefundTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew refund direct from cpfp refund tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewRefundTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.DirectFromCpfpRefundTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock+spark.DirectTimelockOffset)|unsupportedHighBit)
				_, err := constructRenewRefundTransactions(leafNode, parentNode, signingJob)
				return err
			},
		},
		{
			name: "renew zero node tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeZeroTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.NodeTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, spark.ZeroTimelock|unsupportedHighBit)
				_, err := constructRenewZeroNodeTransactions(leafNode, signingJob)
				return err
			},
		},
		{
			name: "renew zero refund tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeZeroTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.RefundTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, spark.InitialTimeLock|unsupportedHighBit)
				_, err := constructRenewZeroNodeTransactions(leafNode, signingJob)
				return err
			},
		},
		{
			name: "renew zero direct node tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeZeroTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.DirectNodeTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, spark.DirectTimelockOffset|unsupportedHighBit)
				_, err := constructRenewZeroNodeTransactions(leafNode, signingJob)
				return err
			},
		},
		{
			name: "renew zero direct from cpfp refund tx",
			run: func(t *testing.T, ctx context.Context, rng io.Reader, leafNode *ent.TreeNode, parentNode *ent.TreeNode) error {
				signingJob := createTestRenewNodeZeroTimelockSigningJob(t, rng, leafNode, 0)
				signingJob.DirectFromCpfpRefundTxSigningJob.RawTx = createValidTestTransactionBytesWithSequence(t, (spark.InitialTimeLock+spark.DirectTimelockOffset)|unsupportedHighBit)
				_, err := constructRenewZeroNodeTransactions(leafNode, signingJob)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, rng, leafNode, parentNode := newFixture(t)
			err := tt.run(t, ctx, rng, leafNode, parentNode)
			require.ErrorContains(t, err, "unsupported high bits 0x00010000")
		})
	}
}

func serializeRenewTestTx(t *testing.T, tx *wire.MsgTx) []byte {
	t.Helper()

	var buf bytes.Buffer
	err := tx.Serialize(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func mutateRenewTestTx(t *testing.T, tx *wire.MsgTx, mutate func(*wire.MsgTx)) []byte {
	t.Helper()

	clonedTx, err := common.TxFromRawTxBytes(serializeRenewTestTx(t, tx))
	require.NoError(t, err)
	mutate(clonedTx)
	return serializeRenewTestTx(t, clonedTx)
}

func renewTestWrongOutputValue(t *testing.T, tx *wire.MsgTx) []byte {
	t.Helper()

	return mutateRenewTestTx(t, tx, func(clonedTx *wire.MsgTx) {
		require.NotEmpty(t, clonedTx.TxOut)
		if clonedTx.TxOut[0].Value > 0 {
			clonedTx.TxOut[0].Value--
		} else {
			clonedTx.TxOut[0].Value++
		}
	})
}

func renewTestWrongPrevoutIndex(t *testing.T, tx *wire.MsgTx) []byte {
	t.Helper()

	return mutateRenewTestTx(t, tx, func(clonedTx *wire.MsgTx) {
		require.NotEmpty(t, clonedTx.TxIn)
		clonedTx.TxIn[0].PreviousOutPoint.Index++
	})
}

func createRenewValidationFixture(t *testing.T, leafNodeSequence uint32, leafRefundSequence uint32) (context.Context, io.Reader, *ent.TreeNode, *ent.TreeNode) {
	t.Helper()

	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{71})
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerIdentityPubKey)
	parent := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, 0)
	leaf := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, parent, 0)
	leaf, err = leaf.Update().
		SetRawTx(createValidTestTransactionBytesWithSequence(t, leafNodeSequence)).
		SetRawRefundTx(createValidTestTransactionBytesWithSequence(t, leafRefundSequence)).
		Save(ctx)
	require.NoError(t, err)

	return ctx, rng, leaf, parent
}

func setValidRenewNodeTimelockRawTxs(t *testing.T, signingJob *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions) {
	t.Helper()

	signingJob.SplitNodeTxSigningJob.RawTx = serializeRenewTestTx(t, txs.SplitNodeTx)
	signingJob.NodeTxSigningJob.RawTx = serializeRenewTestTx(t, txs.NodeTx)
	signingJob.RefundTxSigningJob.RawTx = serializeRenewTestTx(t, txs.RefundTx)
	signingJob.SplitNodeDirectTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectSplitNodeTx)
	signingJob.DirectNodeTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectNodeTx)
	signingJob.DirectRefundTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectRefundTx)
	signingJob.DirectFromCpfpRefundTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectFromCpfpRefundTx)
}

func setValidRenewRefundTimelockRawTxs(t *testing.T, signingJob *pb.RenewRefundTimelockSigningJob, txs *RenewRefundTransactions) {
	t.Helper()

	signingJob.NodeTxSigningJob.RawTx = serializeRenewTestTx(t, txs.NodeTx)
	signingJob.RefundTxSigningJob.RawTx = serializeRenewTestTx(t, txs.RefundTx)
	signingJob.DirectNodeTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectNodeTx)
	signingJob.DirectRefundTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectRefundTx)
	signingJob.DirectFromCpfpRefundTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectFromCpfpRefundTx)
}

func setValidRenewNodeZeroTimelockRawTxs(t *testing.T, signingJob *pb.RenewNodeZeroTimelockSigningJob, txs *RenewZeroNodeTransactions) {
	t.Helper()

	signingJob.NodeTxSigningJob.RawTx = serializeRenewTestTx(t, txs.NodeTx)
	signingJob.RefundTxSigningJob.RawTx = serializeRenewTestTx(t, txs.RefundTx)
	signingJob.DirectNodeTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectNodeTx)
	signingJob.DirectFromCpfpRefundTxSigningJob.RawTx = serializeRenewTestTx(t, txs.DirectFromCpfpRefundTx)
}

func TestValidateAndConstructRenewNodeTimelockRejectsMismatchedRawTxs(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*testing.T, *wire.MsgTx) []byte
	}{
		{name: "wrong output value", mutate: renewTestWrongOutputValue},
		{name: "wrong previous outpoint", mutate: renewTestWrongPrevoutIndex},
	}
	txFields := []struct {
		name string
		set  func(*testing.T, *pb.RenewNodeTimelockSigningJob, *RenewNodeTransactions, func(*testing.T, *wire.MsgTx) []byte)
	}{
		{name: "split node tx", set: func(t *testing.T, job *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.SplitNodeTxSigningJob.RawTx = mutate(t, txs.SplitNodeTx)
		}},
		{name: "node tx", set: func(t *testing.T, job *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.NodeTxSigningJob.RawTx = mutate(t, txs.NodeTx)
		}},
		{name: "refund tx", set: func(t *testing.T, job *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.RefundTxSigningJob.RawTx = mutate(t, txs.RefundTx)
		}},
		{name: "direct split node tx", set: func(t *testing.T, job *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.SplitNodeDirectTxSigningJob.RawTx = mutate(t, txs.DirectSplitNodeTx)
		}},
		{name: "direct node tx", set: func(t *testing.T, job *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.DirectNodeTxSigningJob.RawTx = mutate(t, txs.DirectNodeTx)
		}},
		{name: "direct refund tx", set: func(t *testing.T, job *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.DirectRefundTxSigningJob.RawTx = mutate(t, txs.DirectRefundTx)
		}},
		{name: "direct from cpfp refund tx", set: func(t *testing.T, job *pb.RenewNodeTimelockSigningJob, txs *RenewNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.DirectFromCpfpRefundTxSigningJob.RawTx = mutate(t, txs.DirectFromCpfpRefundTx)
		}},
	}

	for _, txField := range txFields {
		for _, mutation := range mutations {
			t.Run(txField.name+" "+mutation.name, func(t *testing.T) {
				ctx, rng, leaf, parent := createRenewValidationFixture(t, spark.RenewTimelockThreshold|spark.ZeroSequence, spark.RenewTimelockThreshold|spark.ZeroSequence)
				signingJob := createTestRenewNodeTimelockSigningJob(t, rng, leaf, 0)
				renewTxs, err := constructRenewNodeTransactions(leaf, parent, signingJob)
				require.NoError(t, err)
				setValidRenewNodeTimelockRawTxs(t, signingJob, renewTxs)
				txField.set(t, signingJob, renewTxs, mutation.mutate)

				_, _, _, err = validateAndConstructNodeTimelock(ctx, leaf, signingJob)
				require.ErrorContains(t, err, "user transaction validation failed")
			})
		}
	}
}

func TestValidateAndConstructRenewRefundTimelockRejectsMismatchedRawTxs(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*testing.T, *wire.MsgTx) []byte
	}{
		{name: "wrong output value", mutate: renewTestWrongOutputValue},
		{name: "wrong previous outpoint", mutate: renewTestWrongPrevoutIndex},
	}
	txFields := []struct {
		name string
		set  func(*testing.T, *pb.RenewRefundTimelockSigningJob, *RenewRefundTransactions, func(*testing.T, *wire.MsgTx) []byte)
	}{
		{name: "node tx", set: func(t *testing.T, job *pb.RenewRefundTimelockSigningJob, txs *RenewRefundTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.NodeTxSigningJob.RawTx = mutate(t, txs.NodeTx)
		}},
		{name: "refund tx", set: func(t *testing.T, job *pb.RenewRefundTimelockSigningJob, txs *RenewRefundTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.RefundTxSigningJob.RawTx = mutate(t, txs.RefundTx)
		}},
		{name: "direct node tx", set: func(t *testing.T, job *pb.RenewRefundTimelockSigningJob, txs *RenewRefundTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.DirectNodeTxSigningJob.RawTx = mutate(t, txs.DirectNodeTx)
		}},
		{name: "direct refund tx", set: func(t *testing.T, job *pb.RenewRefundTimelockSigningJob, txs *RenewRefundTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.DirectRefundTxSigningJob.RawTx = mutate(t, txs.DirectRefundTx)
		}},
		{name: "direct from cpfp refund tx", set: func(t *testing.T, job *pb.RenewRefundTimelockSigningJob, txs *RenewRefundTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.DirectFromCpfpRefundTxSigningJob.RawTx = mutate(t, txs.DirectFromCpfpRefundTx)
		}},
	}

	for _, txField := range txFields {
		for _, mutation := range mutations {
			t.Run(txField.name+" "+mutation.name, func(t *testing.T) {
				ctx, rng, leaf, parent := createRenewValidationFixture(t, spark.InitialTimeLock|spark.ZeroSequence, spark.TimeLockInterval|spark.ZeroSequence)
				signingJob := createTestRenewRefundTimelockSigningJob(t, rng, leaf, 0)
				renewTxs, err := constructRenewRefundTransactions(leaf, parent, signingJob)
				require.NoError(t, err)
				setValidRenewRefundTimelockRawTxs(t, signingJob, renewTxs)
				txField.set(t, signingJob, renewTxs, mutation.mutate)

				_, _, _, err = validateAndConstructRefundTimelock(ctx, leaf, signingJob)
				require.ErrorContains(t, err, "user transaction validation failed")
			})
		}
	}
}

func TestValidateAndConstructRenewNodeZeroTimelockRejectsMismatchedRawTxs(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*testing.T, *wire.MsgTx) []byte
	}{
		{name: "wrong output value", mutate: renewTestWrongOutputValue},
		{name: "wrong previous outpoint", mutate: renewTestWrongPrevoutIndex},
	}
	txFields := []struct {
		name string
		set  func(*testing.T, *pb.RenewNodeZeroTimelockSigningJob, *RenewZeroNodeTransactions, func(*testing.T, *wire.MsgTx) []byte)
	}{
		{name: "node tx", set: func(t *testing.T, job *pb.RenewNodeZeroTimelockSigningJob, txs *RenewZeroNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.NodeTxSigningJob.RawTx = mutate(t, txs.NodeTx)
		}},
		{name: "refund tx", set: func(t *testing.T, job *pb.RenewNodeZeroTimelockSigningJob, txs *RenewZeroNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.RefundTxSigningJob.RawTx = mutate(t, txs.RefundTx)
		}},
		{name: "direct node tx", set: func(t *testing.T, job *pb.RenewNodeZeroTimelockSigningJob, txs *RenewZeroNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.DirectNodeTxSigningJob.RawTx = mutate(t, txs.DirectNodeTx)
		}},
		{name: "direct from cpfp refund tx", set: func(t *testing.T, job *pb.RenewNodeZeroTimelockSigningJob, txs *RenewZeroNodeTransactions, mutate func(*testing.T, *wire.MsgTx) []byte) {
			job.DirectFromCpfpRefundTxSigningJob.RawTx = mutate(t, txs.DirectFromCpfpRefundTx)
		}},
	}

	for _, txField := range txFields {
		for _, mutation := range mutations {
			t.Run(txField.name+" "+mutation.name, func(t *testing.T) {
				_, rng, leaf, _ := createRenewValidationFixture(t, spark.ZeroTimelock, spark.TimeLockInterval|spark.ZeroSequence)
				signingJob := createTestRenewNodeZeroTimelockSigningJob(t, rng, leaf, 0)
				renewTxs, err := constructRenewZeroNodeTransactions(leaf, signingJob)
				require.NoError(t, err)
				setValidRenewNodeZeroTimelockRawTxs(t, signingJob, renewTxs)
				txField.set(t, signingJob, renewTxs, mutation.mutate)

				_, _, err = validateAndConstructNodeZeroTimelock(leaf, signingJob)
				require.ErrorContains(t, err, "user transaction validation failed")
			})
		}
	}
}

func TestConstructRenewRefundTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tests := []struct {
		name       string
		updateBits uint32
	}{
		{
			name:       "normal case",
			updateBits: 0,
		},
		{
			name:       "30th bit set",
			updateBits: (1 << 30),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test data
			ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
			tree := createTestRenewTree(t, ctx, ownerPubKey)

			// Create parent node
			parentNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, tt.updateBits)

			// Create leaf node with parent
			leafNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, parentNode, tt.updateBits)

			// Get expected pk scripts
			expectedVerifyingPkScript, err := common.P2TRScriptFromPubKey(leafNode.VerifyingPubkey)
			require.NoError(t, err)
			expectedOwnerSigningPkScript, err := common.P2TRScriptFromPubKey(leafNode.OwnerSigningPubkey)
			require.NoError(t, err)

			// Create a test signing job with the specific updateBits
			signingJob := createTestRenewRefundTimelockSigningJob(t, rng, leafNode, tt.updateBits)

			// Test the function
			refundTxs, err := constructRenewRefundTransactions(leafNode, parentNode, signingJob)
			require.NoError(t, err)

			// Parse parent tx to get expected values
			parentTx, err := common.TxFromRawTxBytes(parentNode.RawTx)
			require.NoError(t, err)
			parentAmount := parentTx.TxOut[0].Value

			// Parse leaf tx to get sequence information
			leafTx, err := common.TxFromRawTxBytes(leafNode.RawTx)
			require.NoError(t, err)
			expectedNodeSequence, expectedDirectNodeSequence, err := bitcointransaction.NextSequence(leafTx.TxIn[0].Sequence)
			require.NoError(t, err)

			// Verify node transaction
			assert.NotNil(t, refundTxs.NodeTx)
			assert.Len(t, refundTxs.NodeTx.TxIn, 1)
			assert.Len(t, refundTxs.NodeTx.TxOut, 2) // main output + ephemeral anchor
			assert.Equal(t, expectedNodeSequence, refundTxs.NodeTx.TxIn[0].Sequence)
			assert.Equal(t, parentTx.TxHash(), refundTxs.NodeTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, parentAmount, refundTxs.NodeTx.TxOut[0].Value)
			// Verify main output pk script
			assert.Equal(t, expectedVerifyingPkScript, refundTxs.NodeTx.TxOut[0].PkScript)
			// Verify second output is ephemeral anchor
			assert.Equal(t, int64(0), refundTxs.NodeTx.TxOut[1].Value)
			assert.Equal(t, common.EphemeralAnchorOutput().PkScript, refundTxs.NodeTx.TxOut[1].PkScript)

			// Verify refund transaction
			assert.NotNil(t, refundTxs.RefundTx)
			assert.Len(t, refundTxs.RefundTx.TxIn, 1)
			assert.Len(t, refundTxs.RefundTx.TxOut, 2) // main output + ephemeral anchor
			assert.Equal(t, spark.InitialTimeLock|tt.updateBits, refundTxs.RefundTx.TxIn[0].Sequence)
			assert.Equal(t, refundTxs.NodeTx.TxHash(), refundTxs.RefundTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, parentAmount, refundTxs.RefundTx.TxOut[0].Value)
			// Verify main output pk script
			assert.Equal(t, expectedOwnerSigningPkScript, refundTxs.RefundTx.TxOut[0].PkScript)
			// Verify second output is ephemeral anchor
			assert.Equal(t, int64(0), refundTxs.RefundTx.TxOut[1].Value)
			assert.Equal(t, common.EphemeralAnchorOutput().PkScript, refundTxs.RefundTx.TxOut[1].PkScript)

			// Verify direct node transaction
			assert.NotNil(t, refundTxs.DirectNodeTx)
			assert.Len(t, refundTxs.DirectNodeTx.TxIn, 1)
			assert.Len(t, refundTxs.DirectNodeTx.TxOut, 1)
			assert.Equal(t, expectedDirectNodeSequence, refundTxs.DirectNodeTx.TxIn[0].Sequence)
			assert.Equal(t, parentTx.TxHash(), refundTxs.DirectNodeTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(parentAmount), refundTxs.DirectNodeTx.TxOut[0].Value)
			assert.Equal(t, expectedVerifyingPkScript, refundTxs.DirectNodeTx.TxOut[0].PkScript)

			// Verify direct refund transaction
			assert.NotNil(t, refundTxs.DirectRefundTx)
			assert.Len(t, refundTxs.DirectRefundTx.TxIn, 1)
			assert.Len(t, refundTxs.DirectRefundTx.TxOut, 1)
			assert.Equal(t, (spark.InitialTimeLock+spark.DirectTimelockOffset)|tt.updateBits, refundTxs.DirectRefundTx.TxIn[0].Sequence)
			assert.Equal(t, refundTxs.DirectNodeTx.TxHash(), refundTxs.DirectRefundTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(common.MaybeApplyFee(parentAmount)), refundTxs.DirectRefundTx.TxOut[0].Value)
			assert.Equal(t, expectedOwnerSigningPkScript, refundTxs.DirectRefundTx.TxOut[0].PkScript)

			// Verify direct from CPFP refund transaction
			assert.NotNil(t, refundTxs.DirectFromCpfpRefundTx)
			assert.Len(t, refundTxs.DirectFromCpfpRefundTx.TxIn, 1)
			assert.Len(t, refundTxs.DirectFromCpfpRefundTx.TxOut, 1)
			assert.Equal(t, (spark.InitialTimeLock+spark.DirectTimelockOffset)|tt.updateBits, refundTxs.DirectFromCpfpRefundTx.TxIn[0].Sequence)
			assert.Equal(t, refundTxs.NodeTx.TxHash(), refundTxs.DirectFromCpfpRefundTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(parentAmount), refundTxs.DirectFromCpfpRefundTx.TxOut[0].Value)
			assert.Equal(t, expectedOwnerSigningPkScript, refundTxs.DirectFromCpfpRefundTx.TxOut[0].PkScript)
		})
	}
}

func TestConstructRenewZeroNodeTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tests := []struct {
		name       string
		updateBits uint32
	}{
		{
			name:       "normal case",
			updateBits: 0,
		},
		{
			name:       "30th bit set",
			updateBits: (1 << 30),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test data
			ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
			tree := createTestRenewTree(t, ctx, ownerPubKey)

			// Create leaf node (no parent needed for zero timelock)
			leafNode := createTestRenewTreeNode(t, ctx, rng, dbClient, tree, keyshare, nil, tt.updateBits)

			// Get expected pk scripts
			expectedVerifyingPkScript, err := common.P2TRScriptFromPubKey(leafNode.VerifyingPubkey)
			require.NoError(t, err)
			expectedOwnerSigningPkScript, err := common.P2TRScriptFromPubKey(leafNode.OwnerSigningPubkey)
			require.NoError(t, err)

			// Create a test signing job with the specific updateBits
			signingJob := createTestRenewNodeZeroTimelockSigningJob(t, rng, leafNode, tt.updateBits)

			// Test the function
			zeroTxs, err := constructRenewZeroNodeTransactions(leafNode, signingJob)
			require.NoError(t, err)

			// Parse leaf tx to get expected values
			leafTx, err := common.TxFromRawTxBytes(leafNode.RawTx)
			require.NoError(t, err)
			leafAmount := leafTx.TxOut[0].Value

			// Verify new node transaction (with zero sequence)
			assert.NotNil(t, zeroTxs.NodeTx)
			assert.Len(t, zeroTxs.NodeTx.TxIn, 1)
			assert.Len(t, zeroTxs.NodeTx.TxOut, 2) // main output + ephemeral anchor
			assert.Equal(t, spark.ZeroTimelock|tt.updateBits, zeroTxs.NodeTx.TxIn[0].Sequence)
			assert.Equal(t, leafTx.TxHash(), zeroTxs.NodeTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, leafAmount, zeroTxs.NodeTx.TxOut[0].Value)
			// Verify main output pk script
			assert.Equal(t, expectedVerifyingPkScript, zeroTxs.NodeTx.TxOut[0].PkScript)
			// Verify second output is ephemeral anchor
			assert.Equal(t, int64(0), zeroTxs.NodeTx.TxOut[1].Value)
			assert.Equal(t, common.EphemeralAnchorOutput().PkScript, zeroTxs.NodeTx.TxOut[1].PkScript)

			// Verify refund transaction (with initial sequence)
			assert.NotNil(t, zeroTxs.RefundTx)
			assert.Len(t, zeroTxs.RefundTx.TxIn, 1)
			assert.Len(t, zeroTxs.RefundTx.TxOut, 2) // main output + ephemeral anchor
			assert.Equal(t, spark.InitialTimeLock|tt.updateBits, zeroTxs.RefundTx.TxIn[0].Sequence)
			assert.Equal(t, zeroTxs.NodeTx.TxHash(), zeroTxs.RefundTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, leafAmount, zeroTxs.RefundTx.TxOut[0].Value)
			// Verify main output pk script
			assert.Equal(t, expectedOwnerSigningPkScript, zeroTxs.RefundTx.TxOut[0].PkScript)
			// Verify second output is ephemeral anchor
			assert.Equal(t, int64(0), zeroTxs.RefundTx.TxOut[1].Value)
			assert.Equal(t, common.EphemeralAnchorOutput().PkScript, zeroTxs.RefundTx.TxOut[1].PkScript)

			// Verify direct node transaction
			assert.NotNil(t, zeroTxs.DirectNodeTx)
			assert.Len(t, zeroTxs.DirectNodeTx.TxIn, 1)
			assert.Len(t, zeroTxs.DirectNodeTx.TxOut, 1)
			assert.Equal(t, spark.DirectTimelockOffset|tt.updateBits, zeroTxs.DirectNodeTx.TxIn[0].Sequence)
			assert.Equal(t, leafTx.TxHash(), zeroTxs.DirectNodeTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(leafAmount), zeroTxs.DirectNodeTx.TxOut[0].Value)
			assert.Equal(t, expectedVerifyingPkScript, zeroTxs.DirectNodeTx.TxOut[0].PkScript)

			// Verify direct from CPFP refund transaction
			assert.NotNil(t, zeroTxs.DirectFromCpfpRefundTx)
			assert.Len(t, zeroTxs.DirectFromCpfpRefundTx.TxIn, 1)
			assert.Len(t, zeroTxs.DirectFromCpfpRefundTx.TxOut, 1)
			assert.Equal(t, (spark.InitialTimeLock+spark.DirectTimelockOffset)|tt.updateBits, zeroTxs.DirectFromCpfpRefundTx.TxIn[0].Sequence)
			assert.Equal(t, zeroTxs.NodeTx.TxHash(), zeroTxs.DirectFromCpfpRefundTx.TxIn[0].PreviousOutPoint.Hash)
			assert.Equal(t, common.MaybeApplyFee(leafAmount), zeroTxs.DirectFromCpfpRefundTx.TxOut[0].Value)
			assert.Equal(t, expectedOwnerSigningPkScript, zeroTxs.DirectFromCpfpRefundTx.TxOut[0].PkScript)
		})
	}
}

func TestValidateRenewNodeTimelocks(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerPubKey)

	tests := []struct {
		name           string
		nodeSequence   uint32
		refundSequence uint32
		expectError    bool
		errorContains  string
	}{
		{
			name:           "valid timelocks - both at 300",
			nodeSequence:   300,
			refundSequence: 300,
			expectError:    false,
		},
		{
			name:           "invalid refund timelock - zero",
			nodeSequence:   0,
			refundSequence: 0,
			expectError:    true,
			errorContains:  "refund transaction sequence must be at least 100 for renewal",
		},
		{
			name:           "invalid refund timelock - below TimeLockInterval",
			nodeSequence:   200,
			refundSequence: 99,
			expectError:    true,
			errorContains:  "refund transaction sequence must be at least 100 for renewal",
		},
		{
			name:           "valid refund timelock - exactly TimeLockInterval",
			nodeSequence:   200,
			refundSequence: 100,
			expectError:    false,
		},
		{
			name:           "valid timelocks - node 150, refund 200",
			nodeSequence:   150,
			refundSequence: 200,
			expectError:    false,
		},
		{
			name:           "invalid node timelock - too high",
			nodeSequence:   301,
			refundSequence: 200,
			expectError:    true,
			errorContains:  "node transaction sequence must be less than or equal to 300",
		},
		{
			name:           "invalid refund timelock - too high",
			nodeSequence:   200,
			refundSequence: 301,
			expectError:    true,
			errorContains:  "refund transaction sequence must be less than or equal to 300",
		},
		{
			name:           "both timelocks invalid",
			nodeSequence:   500,
			refundSequence: 400,
			expectError:    true,
			errorContains:  "node transaction sequence must be less than or equal to 300",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create leaf node with specific sequences
			verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

			nodeTxMsg, err := sparktesting.CreateTestP2TRTransactionWithSequence(t, verifyingPubKey, tt.nodeSequence, 100000)
			require.NoError(t, err)
			nodeTx, err := common.SerializeTx(nodeTxMsg)
			require.NoError(t, err)

			refundTxMsg, err := sparktesting.CreateTestP2TRTransactionWithSequence(t, ownerSigningPubKey, tt.refundSequence, 100000)
			require.NoError(t, err)
			refundTx, err := common.SerializeTx(refundTxMsg)
			require.NoError(t, err)

			leafNode := tx.TreeNode.Create().
				SetStatus(st.TreeNodeStatusAvailable).
				SetTree(tree).
				SetNetwork(tree.Network).
				SetSigningKeyshare(keyshare).
				SetValue(100000).
				SetVerifyingPubkey(verifyingPubKey).
				SetOwnerIdentityPubkey(ownerPubKey).
				SetOwnerSigningPubkey(ownerSigningPubKey).
				SetRawTx(nodeTx).
				SetRawRefundTx(refundTx).
				SetDirectTx(nodeTx).
				SetDirectRefundTx(refundTx).
				SetDirectFromCpfpRefundTx(refundTx).
				SetVout(0)

			leaf, err := leafNode.Save(ctx)
			require.NoError(t, err)

			// Test validation
			err = validateRenewNodeTimelocks(leaf)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateRenewRefundTimelock(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerPubKey)

	tests := []struct {
		name           string
		nodeSequence   uint32
		refundSequence uint32
		expectError    bool
		errorContains  string
	}{
		{
			name:           "valid refund timelock - at 300",
			nodeSequence:   2000,
			refundSequence: 300,
			expectError:    false,
		},
		{
			name:           "invalid refund timelock - zero",
			nodeSequence:   2000,
			refundSequence: 0,
			expectError:    true,
			errorContains:  "refund transaction sequence must be at least 100 for renewal",
		},
		{
			name:           "valid node timelock at 200 - should pass",
			nodeSequence:   200,
			refundSequence: 100,
			expectError:    false,
		},
		{
			name:           "invalid refund timelock - too high",
			nodeSequence:   2000,
			refundSequence: 301,
			expectError:    true,
			errorContains:  "refund transaction sequence must be less than or equal to 300",
		},
		{
			name:           "invalid node timelock at 100 - should fail",
			nodeSequence:   100,
			refundSequence: 300,
			expectError:    true,
			errorContains:  "failed to decrement node tx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create leaf node with specific node and refund sequences
			verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

			nodeTxMsg, err := sparktesting.CreateTestP2TRTransactionWithSequence(t, verifyingPubKey, tt.nodeSequence, 100000)
			require.NoError(t, err)
			nodeTx, err := common.SerializeTx(nodeTxMsg)
			require.NoError(t, err)

			refundTxMsg, err := sparktesting.CreateTestP2TRTransactionWithSequence(t, ownerSigningPubKey, tt.refundSequence, 100000)
			require.NoError(t, err)
			refundTx, err := common.SerializeTx(refundTxMsg)
			require.NoError(t, err)

			leafNode := tx.TreeNode.Create().
				SetStatus(st.TreeNodeStatusAvailable).
				SetTree(tree).
				SetNetwork(tree.Network).
				SetSigningKeyshare(keyshare).
				SetValue(100000).
				SetVerifyingPubkey(verifyingPubKey).
				SetOwnerIdentityPubkey(ownerPubKey).
				SetOwnerSigningPubkey(ownerSigningPubKey).
				SetRawTx(nodeTx).
				SetRawRefundTx(refundTx).
				SetDirectTx(nodeTx).
				SetDirectRefundTx(refundTx).
				SetDirectFromCpfpRefundTx(refundTx).
				SetVout(0)

			leaf, err := leafNode.Save(ctx)
			require.NoError(t, err)

			// Test validation
			err = validateRenewRefundTimelock(leaf)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateRenewNodeZeroTimelock(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerPubKey)

	tests := []struct {
		name           string
		nodeSequence   uint32
		refundSequence uint32
		expectError    bool
		errorContains  string
	}{
		{
			name:           "valid zero timelock - node 0, refund 300",
			nodeSequence:   0,
			refundSequence: 300,
			expectError:    false,
		},
		{
			name:           "invalid zero timelock - node 0, refund 0",
			nodeSequence:   0,
			refundSequence: 0,
			expectError:    true,
			errorContains:  "refund transaction sequence must be at least 100 for renewal",
		},
		{
			name:           "valid zero timelock - node 0, refund 150",
			nodeSequence:   0,
			refundSequence: 150,
			expectError:    false,
		},
		{
			name:           "invalid refund timelock - below TimeLockInterval",
			nodeSequence:   0,
			refundSequence: 99,
			expectError:    true,
			errorContains:  "refund transaction sequence must be at least 100 for renewal",
		},
		{
			name:           "valid refund timelock - exactly TimeLockInterval",
			nodeSequence:   0,
			refundSequence: 100,
			expectError:    false,
		},
		{
			name:           "invalid node timelock - not zero",
			nodeSequence:   1,
			refundSequence: 200,
			expectError:    true,
			errorContains:  "node transaction sequence must be 0 for zero timelock renewal",
		},
		{
			name:           "invalid refund timelock - too high",
			nodeSequence:   0,
			refundSequence: 301,
			expectError:    true,
			errorContains:  "refund transaction sequence must be less than or equal to 300",
		},
		{
			name:           "invalid node timelock - much higher than zero",
			nodeSequence:   100,
			refundSequence: 200,
			expectError:    true,
			errorContains:  "node transaction sequence must be 0 for zero timelock renewal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create leaf node with specific sequences
			verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

			nodeTxMsg, err := sparktesting.CreateTestP2TRTransactionWithSequence(t, verifyingPubKey, tt.nodeSequence, 100000)
			require.NoError(t, err)
			nodeTx, err := common.SerializeTx(nodeTxMsg)
			require.NoError(t, err)

			refundTxMsg, err := sparktesting.CreateTestP2TRTransactionWithSequence(t, ownerSigningPubKey, tt.refundSequence, 100000)
			require.NoError(t, err)
			refundTx, err := common.SerializeTx(refundTxMsg)
			require.NoError(t, err)

			leafNode := tx.TreeNode.Create().
				SetStatus(st.TreeNodeStatusAvailable).
				SetTree(tree).
				SetNetwork(tree.Network).
				SetSigningKeyshare(keyshare).
				SetValue(100000).
				SetVerifyingPubkey(verifyingPubKey).
				SetOwnerIdentityPubkey(ownerPubKey).
				SetOwnerSigningPubkey(ownerSigningPubKey).
				SetRawTx(nodeTx).
				SetRawRefundTx(refundTx).
				SetDirectTx(nodeTx).
				SetDirectRefundTx(refundTx).
				SetDirectFromCpfpRefundTx(refundTx).
				SetVout(0)

			leaf, err := leafNode.Save(ctx)
			require.NoError(t, err)

			// Test validation
			err = validateRenewNodeZeroTimelock(leaf)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
