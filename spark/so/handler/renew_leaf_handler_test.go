package handler

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestRenewLeafHandler() *RenewLeafHandler {
	config := &so.Config{
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	return NewRenewLeafHandler(config)
}

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

	tree, err := tx.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(st.NetworkRegtest).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		// Simply setting random bytes for unique tree
		SetBaseTxid(ownerIdentityPubKey.ToBTCEC().SerializeCompressed()).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return tree
}

func createTestRenewTreeNode(t *testing.T, ctx context.Context, rng io.Reader, tx *ent.Tx, tree *ent.Tree, keyshare *ent.SigningKeyshare, parent *ent.TreeNode) *ent.TreeNode {
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create transactions with the appropriate keys
	verifyingAddr, err := common.P2TRAddressFromPublicKey(verifyingPubKey, common.Regtest)
	require.NoError(t, err)
	nodeTxMsg, err := sparktesting.CreateTestP2TRTransaction(verifyingAddr, 100000)
	require.NoError(t, err)
	nodeTx, err := common.SerializeTx(nodeTxMsg)
	require.NoError(t, err)

	ownerSigningAddr, err := common.P2TRAddressFromPublicKey(ownerSigningPubKey, common.Regtest)
	require.NoError(t, err)
	refundTxMsg, err := sparktesting.CreateTestP2TRTransaction(ownerSigningAddr, 100000)
	require.NoError(t, err)
	refundTx, err := common.SerializeTx(refundTxMsg)
	require.NoError(t, err)

	nodeCreate := tx.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
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

func TestConstructRenewNodeTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
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
			parentNode := createTestRenewTreeNode(t, ctx, rng, tx, tree, keyshare, nil)

			// Create leaf node with parent
			leafNode := createTestRenewTreeNode(t, ctx, rng, tx, tree, keyshare, parentNode)

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
			assert.Equal(t, uint32(spark.DirectTimelockOffset)|tt.updateBits, renewTxs.DirectSplitNodeTx.TxIn[0].Sequence)
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

func TestConstructRenewRefundTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	handler := createTestRenewLeafHandler()
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerPubKey)

	// Create parent node
	parentNode := createTestRenewTreeNode(t, ctx, rng, tx, tree, keyshare, nil)

	// Create leaf node with parent
	leafNode := createTestRenewTreeNode(t, ctx, rng, tx, tree, keyshare, parentNode)

	// Get expected pk scripts
	expectedVerifyingPkScript, err := common.P2TRScriptFromPubKey(leafNode.VerifyingPubkey)
	require.NoError(t, err)
	expectedOwnerSigningPkScript, err := common.P2TRScriptFromPubKey(leafNode.OwnerSigningPubkey)
	require.NoError(t, err)

	// Test the function
	refundTxs, err := handler.constructRenewRefundTransactions(leafNode, parentNode)
	require.NoError(t, err)

	// Parse parent tx to get expected values
	parentTx, err := common.TxFromRawTxBytes(parentNode.RawTx)
	require.NoError(t, err)
	parentAmount := parentTx.TxOut[0].Value

	// Parse leaf tx to get sequence information
	leafTx, err := common.TxFromRawTxBytes(leafNode.RawTx)
	require.NoError(t, err)
	expectedSequence, err := spark.NextSequence(leafTx.TxIn[0].Sequence)
	require.NoError(t, err)

	// Verify node transaction
	assert.NotNil(t, refundTxs.NodeTx)
	assert.Len(t, refundTxs.NodeTx.TxIn, 1)
	assert.Len(t, refundTxs.NodeTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, expectedSequence, refundTxs.NodeTx.TxIn[0].Sequence)
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
	assert.Equal(t, spark.InitialSequence(), refundTxs.RefundTx.TxIn[0].Sequence)
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
	assert.Equal(t, expectedSequence+spark.DirectTimelockOffset, refundTxs.DirectNodeTx.TxIn[0].Sequence)
	assert.Equal(t, parentTx.TxHash(), refundTxs.DirectNodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(parentAmount), refundTxs.DirectNodeTx.TxOut[0].Value)
	assert.Equal(t, expectedVerifyingPkScript, refundTxs.DirectNodeTx.TxOut[0].PkScript)

	// Verify direct refund transaction
	assert.NotNil(t, refundTxs.DirectRefundTx)
	assert.Len(t, refundTxs.DirectRefundTx.TxIn, 1)
	assert.Len(t, refundTxs.DirectRefundTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, refundTxs.DirectRefundTx.TxIn[0].Sequence)
	assert.Equal(t, refundTxs.DirectNodeTx.TxHash(), refundTxs.DirectRefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(common.MaybeApplyFee(parentAmount)), refundTxs.DirectRefundTx.TxOut[0].Value)
	assert.Equal(t, expectedOwnerSigningPkScript, refundTxs.DirectRefundTx.TxOut[0].PkScript)

	// Verify direct from CPFP refund transaction
	assert.NotNil(t, refundTxs.DirectFromCpfpRefundTx)
	assert.Len(t, refundTxs.DirectFromCpfpRefundTx.TxIn, 1)
	assert.Len(t, refundTxs.DirectFromCpfpRefundTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, refundTxs.DirectFromCpfpRefundTx.TxIn[0].Sequence)
	assert.Equal(t, refundTxs.NodeTx.TxHash(), refundTxs.DirectFromCpfpRefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(parentAmount), refundTxs.DirectFromCpfpRefundTx.TxOut[0].Value)
	assert.Equal(t, expectedOwnerSigningPkScript, refundTxs.DirectFromCpfpRefundTx.TxOut[0].PkScript)
}

func TestConstructRenewZeroNodeTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	handler := createTestRenewLeafHandler()
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerPubKey)

	// Create leaf node (no parent needed for zero timelock)
	leafNode := createTestRenewTreeNode(t, ctx, rng, tx, tree, keyshare, nil)

	// Get expected pk scripts
	expectedVerifyingPkScript, err := common.P2TRScriptFromPubKey(leafNode.VerifyingPubkey)
	require.NoError(t, err)
	expectedOwnerSigningPkScript, err := common.P2TRScriptFromPubKey(leafNode.OwnerSigningPubkey)
	require.NoError(t, err)

	// Test the function
	zeroTxs, err := handler.constructRenewZeroNodeTransactions(leafNode)
	require.NoError(t, err)

	// Parse leaf tx to get expected values
	leafTx, err := common.TxFromRawTxBytes(leafNode.RawTx)
	require.NoError(t, err)
	leafAmount := leafTx.TxOut[0].Value

	// Verify new node transaction (with zero sequence)
	assert.NotNil(t, zeroTxs.NodeTx)
	assert.Len(t, zeroTxs.NodeTx.TxIn, 1)
	assert.Len(t, zeroTxs.NodeTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, spark.ZeroSequence, zeroTxs.NodeTx.TxIn[0].Sequence)
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
	assert.Equal(t, spark.InitialSequence(), zeroTxs.RefundTx.TxIn[0].Sequence)
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
	assert.Equal(t, uint32(spark.DirectTimelockOffset), zeroTxs.DirectNodeTx.TxIn[0].Sequence)
	assert.Equal(t, leafTx.TxHash(), zeroTxs.DirectNodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(leafAmount), zeroTxs.DirectNodeTx.TxOut[0].Value)
	assert.Equal(t, expectedVerifyingPkScript, zeroTxs.DirectNodeTx.TxOut[0].PkScript)

	// Verify direct from CPFP refund transaction
	assert.NotNil(t, zeroTxs.DirectFromCpfpRefundTx)
	assert.Len(t, zeroTxs.DirectFromCpfpRefundTx.TxIn, 1)
	assert.Len(t, zeroTxs.DirectFromCpfpRefundTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, zeroTxs.DirectFromCpfpRefundTx.TxIn[0].Sequence)
	assert.Equal(t, zeroTxs.NodeTx.TxHash(), zeroTxs.DirectFromCpfpRefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(leafAmount), zeroTxs.DirectFromCpfpRefundTx.TxOut[0].Value)
	assert.Equal(t, expectedOwnerSigningPkScript, zeroTxs.DirectFromCpfpRefundTx.TxOut[0].PkScript)
}

func TestValidateRenewNodeTimelocks(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	handler := createTestRenewLeafHandler()

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
			name:           "valid timelocks - both at 0",
			nodeSequence:   0,
			refundSequence: 0,
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
			err = handler.validateRenewNodeTimelocks(leaf)

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

	handler := createTestRenewLeafHandler()

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
			name:           "valid refund timelock - at 0",
			nodeSequence:   2000,
			refundSequence: 0,
			expectError:    false,
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
			err = handler.validateRenewRefundTimelock(leaf)

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

	handler := createTestRenewLeafHandler()

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
			name:           "valid zero timelock - node 0, refund 0",
			nodeSequence:   0,
			refundSequence: 0,
			expectError:    false,
		},
		{
			name:           "valid zero timelock - node 0, refund 150",
			nodeSequence:   0,
			refundSequence: 150,
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
			err = handler.validateRenewNodeZeroTimelock(leaf)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
