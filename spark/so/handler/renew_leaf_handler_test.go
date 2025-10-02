package handler

import (
	"context"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
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

func createTestRenewSigningKeyshare(t *testing.T, ctx context.Context, rng io.Reader, tx *ent.Tx) *ent.SigningKeyshare {
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	pubSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

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

func createTestRenewTree(t *testing.T, ctx context.Context, ownerIdentityPubKey keys.Public, tx *ent.Tx) *ent.Tree {
	tree, err := tx.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(st.NetworkRegtest).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetBaseTxid([]byte("test_base_txid")).
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

	handler := createTestRenewLeafHandler()

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng, tx)
	tree := createTestRenewTree(t, ctx, ownerPubKey, tx)

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
	splitNodeTx, extendedNodeTx, refundTx, directSplitNodeTx, directNodeTx, directRefundTx, directFromCpfpRefundTx, err := handler.constructRenewNodeTransactions(leafNode, parentNode)
	require.NoError(t, err)

	// Verify split node transaction
	assert.NotNil(t, splitNodeTx)
	assert.Len(t, splitNodeTx.TxIn, 1)
	assert.Len(t, splitNodeTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, spark.ZeroSequence, splitNodeTx.TxIn[0].Sequence)
	// Verify main output pk script
	assert.Equal(t, expectedVerifyingPkScript, splitNodeTx.TxOut[0].PkScript)
	// Verify second output is ephemeral anchor
	assert.Equal(t, int64(0), splitNodeTx.TxOut[1].Value)
	assert.Equal(t, common.EphemeralAnchorOutput().PkScript, splitNodeTx.TxOut[1].PkScript)

	// Parse parent tx to check values
	parentTx, err := common.TxFromRawTxBytes(parentNode.RawTx)
	require.NoError(t, err)
	parentAmount := parentTx.TxOut[0].Value

	// Split node should use parent tx hash and parent amount
	assert.Equal(t, parentTx.TxHash(), splitNodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, uint32(0), splitNodeTx.TxIn[0].PreviousOutPoint.Index)
	assert.Equal(t, parentAmount, splitNodeTx.TxOut[0].Value)

	// Verify extended node transaction
	assert.NotNil(t, extendedNodeTx)
	assert.Len(t, extendedNodeTx.TxIn, 1)
	assert.Len(t, extendedNodeTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, spark.InitialSequence(), extendedNodeTx.TxIn[0].Sequence)
	assert.Equal(t, splitNodeTx.TxHash(), extendedNodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, parentAmount, extendedNodeTx.TxOut[0].Value)
	// Verify main output pk script
	assert.Equal(t, expectedVerifyingPkScript, extendedNodeTx.TxOut[0].PkScript)
	// Verify second output is ephemeral anchor
	assert.Equal(t, int64(0), extendedNodeTx.TxOut[1].Value)
	assert.Equal(t, common.EphemeralAnchorOutput().PkScript, extendedNodeTx.TxOut[1].PkScript)

	// Verify refund transaction
	assert.NotNil(t, refundTx)
	assert.Len(t, refundTx.TxIn, 1)
	assert.Len(t, refundTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, spark.InitialSequence(), refundTx.TxIn[0].Sequence)
	assert.Equal(t, extendedNodeTx.TxHash(), refundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, parentAmount, refundTx.TxOut[0].Value)
	// Verify main output pk script
	assert.Equal(t, expectedOwnerSigningPkScript, refundTx.TxOut[0].PkScript)
	// Verify second output is ephemeral anchor
	assert.Equal(t, int64(0), refundTx.TxOut[1].Value)
	assert.Equal(t, common.EphemeralAnchorOutput().PkScript, refundTx.TxOut[1].PkScript)

	// Verify direct split node transaction
	assert.NotNil(t, directSplitNodeTx)
	assert.Len(t, directSplitNodeTx.TxIn, 1)
	assert.Len(t, directSplitNodeTx.TxOut, 1)
	assert.Equal(t, uint32(spark.DirectTimelockOffset), directSplitNodeTx.TxIn[0].Sequence)
	assert.Equal(t, parentTx.TxHash(), directSplitNodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(parentAmount), directSplitNodeTx.TxOut[0].Value)
	assert.Equal(t, expectedVerifyingPkScript, directSplitNodeTx.TxOut[0].PkScript)

	// Verify direct node transaction
	assert.NotNil(t, directNodeTx)
	assert.Len(t, directNodeTx.TxIn, 1)
	assert.Len(t, directNodeTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, directNodeTx.TxIn[0].Sequence)
	assert.Equal(t, splitNodeTx.TxHash(), directNodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(parentAmount), directNodeTx.TxOut[0].Value)
	assert.Equal(t, expectedVerifyingPkScript, directNodeTx.TxOut[0].PkScript)

	// Verify direct refund transaction
	assert.NotNil(t, directRefundTx)
	assert.Len(t, directRefundTx.TxIn, 1)
	assert.Len(t, directRefundTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, directRefundTx.TxIn[0].Sequence)
	assert.Equal(t, directNodeTx.TxHash(), directRefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(common.MaybeApplyFee(parentAmount)), directRefundTx.TxOut[0].Value)
	assert.Equal(t, expectedOwnerSigningPkScript, directRefundTx.TxOut[0].PkScript)

	// Verify direct from CPFP refund transaction
	assert.NotNil(t, directFromCpfpRefundTx)
	assert.Len(t, directFromCpfpRefundTx.TxIn, 1)
	assert.Len(t, directFromCpfpRefundTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, directFromCpfpRefundTx.TxIn[0].Sequence)
	assert.Equal(t, extendedNodeTx.TxHash(), directFromCpfpRefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(parentAmount), directFromCpfpRefundTx.TxOut[0].Value)
	assert.Equal(t, expectedOwnerSigningPkScript, directFromCpfpRefundTx.TxOut[0].PkScript)
}

func TestConstructRenewRefundTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	handler := createTestRenewLeafHandler()

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng, tx)
	tree := createTestRenewTree(t, ctx, ownerPubKey, tx)

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
	nodeTx, refundTx, directNodeTx, directRefundTx, directFromCpfpRefundTx, err := handler.constructRenewRefundTransactions(leafNode, parentNode)
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
	assert.NotNil(t, nodeTx)
	assert.Len(t, nodeTx.TxIn, 1)
	assert.Len(t, nodeTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, expectedSequence, nodeTx.TxIn[0].Sequence)
	assert.Equal(t, parentTx.TxHash(), nodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, parentAmount, nodeTx.TxOut[0].Value)
	// Verify main output pk script
	assert.Equal(t, expectedVerifyingPkScript, nodeTx.TxOut[0].PkScript)
	// Verify second output is ephemeral anchor
	assert.Equal(t, int64(0), nodeTx.TxOut[1].Value)
	assert.Equal(t, common.EphemeralAnchorOutput().PkScript, nodeTx.TxOut[1].PkScript)

	// Verify refund transaction
	assert.NotNil(t, refundTx)
	assert.Len(t, refundTx.TxIn, 1)
	assert.Len(t, refundTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, spark.InitialSequence(), refundTx.TxIn[0].Sequence)
	assert.Equal(t, nodeTx.TxHash(), refundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, parentAmount, refundTx.TxOut[0].Value)
	// Verify main output pk script
	assert.Equal(t, expectedOwnerSigningPkScript, refundTx.TxOut[0].PkScript)
	// Verify second output is ephemeral anchor
	assert.Equal(t, int64(0), refundTx.TxOut[1].Value)
	assert.Equal(t, common.EphemeralAnchorOutput().PkScript, refundTx.TxOut[1].PkScript)

	// Verify direct node transaction
	assert.NotNil(t, directNodeTx)
	assert.Len(t, directNodeTx.TxIn, 1)
	assert.Len(t, directNodeTx.TxOut, 1)
	assert.Equal(t, expectedSequence+spark.DirectTimelockOffset, directNodeTx.TxIn[0].Sequence)
	assert.Equal(t, parentTx.TxHash(), directNodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(parentAmount), directNodeTx.TxOut[0].Value)
	assert.Equal(t, expectedVerifyingPkScript, directNodeTx.TxOut[0].PkScript)

	// Verify direct refund transaction
	assert.NotNil(t, directRefundTx)
	assert.Len(t, directRefundTx.TxIn, 1)
	assert.Len(t, directRefundTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, directRefundTx.TxIn[0].Sequence)
	assert.Equal(t, directNodeTx.TxHash(), directRefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(common.MaybeApplyFee(parentAmount)), directRefundTx.TxOut[0].Value)
	assert.Equal(t, expectedOwnerSigningPkScript, directRefundTx.TxOut[0].PkScript)

	// Verify direct from CPFP refund transaction
	assert.NotNil(t, directFromCpfpRefundTx)
	assert.Len(t, directFromCpfpRefundTx.TxIn, 1)
	assert.Len(t, directFromCpfpRefundTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, directFromCpfpRefundTx.TxIn[0].Sequence)
	assert.Equal(t, nodeTx.TxHash(), directFromCpfpRefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(parentAmount), directFromCpfpRefundTx.TxOut[0].Value)
	assert.Equal(t, expectedOwnerSigningPkScript, directFromCpfpRefundTx.TxOut[0].PkScript)
}

func TestConstructRenewZeroNodeTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	handler := createTestRenewLeafHandler()

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng, tx)
	tree := createTestRenewTree(t, ctx, ownerPubKey, tx)

	// Create leaf node (no parent needed for zero timelock)
	leafNode := createTestRenewTreeNode(t, ctx, rng, tx, tree, keyshare, nil)

	// Get expected pk scripts
	expectedVerifyingPkScript, err := common.P2TRScriptFromPubKey(leafNode.VerifyingPubkey)
	require.NoError(t, err)
	expectedOwnerSigningPkScript, err := common.P2TRScriptFromPubKey(leafNode.OwnerSigningPubkey)
	require.NoError(t, err)

	// Test the function
	nodeTx, refundTx, directNodeTx, directFromCpfpRefundTx, err := handler.constructRenewZeroNodeTransactions(leafNode)
	require.NoError(t, err)

	// Parse leaf tx to get expected values
	leafTx, err := common.TxFromRawTxBytes(leafNode.RawTx)
	require.NoError(t, err)
	leafAmount := leafTx.TxOut[0].Value

	// Verify new node transaction (with zero sequence)
	assert.NotNil(t, nodeTx)
	assert.Len(t, nodeTx.TxIn, 1)
	assert.Len(t, nodeTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, spark.ZeroSequence, nodeTx.TxIn[0].Sequence)
	assert.Equal(t, leafTx.TxHash(), nodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, leafAmount, nodeTx.TxOut[0].Value)
	// Verify main output pk script
	assert.Equal(t, expectedVerifyingPkScript, nodeTx.TxOut[0].PkScript)
	// Verify second output is ephemeral anchor
	assert.Equal(t, int64(0), nodeTx.TxOut[1].Value)
	assert.Equal(t, common.EphemeralAnchorOutput().PkScript, nodeTx.TxOut[1].PkScript)

	// Verify refund transaction (with initial sequence)
	assert.NotNil(t, refundTx)
	assert.Len(t, refundTx.TxIn, 1)
	assert.Len(t, refundTx.TxOut, 2) // main output + ephemeral anchor
	assert.Equal(t, spark.InitialSequence(), refundTx.TxIn[0].Sequence)
	assert.Equal(t, nodeTx.TxHash(), refundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, leafAmount, refundTx.TxOut[0].Value)
	// Verify main output pk script
	assert.Equal(t, expectedOwnerSigningPkScript, refundTx.TxOut[0].PkScript)
	// Verify second output is ephemeral anchor
	assert.Equal(t, int64(0), refundTx.TxOut[1].Value)
	assert.Equal(t, common.EphemeralAnchorOutput().PkScript, refundTx.TxOut[1].PkScript)

	// Verify direct node transaction
	assert.NotNil(t, directNodeTx)
	assert.Len(t, directNodeTx.TxIn, 1)
	assert.Len(t, directNodeTx.TxOut, 1)
	assert.Equal(t, uint32(spark.DirectTimelockOffset), directNodeTx.TxIn[0].Sequence)
	assert.Equal(t, leafTx.TxHash(), directNodeTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(leafAmount), directNodeTx.TxOut[0].Value)
	assert.Equal(t, expectedVerifyingPkScript, directNodeTx.TxOut[0].PkScript)

	// Verify direct from CPFP refund transaction
	assert.NotNil(t, directFromCpfpRefundTx)
	assert.Len(t, directFromCpfpRefundTx.TxIn, 1)
	assert.Len(t, directFromCpfpRefundTx.TxOut, 1)
	assert.Equal(t, spark.InitialSequence()+spark.DirectTimelockOffset, directFromCpfpRefundTx.TxIn[0].Sequence)
	assert.Equal(t, nodeTx.TxHash(), directFromCpfpRefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, common.MaybeApplyFee(leafAmount), directFromCpfpRefundTx.TxOut[0].Value)
	assert.Equal(t, expectedOwnerSigningPkScript, directFromCpfpRefundTx.TxOut[0].PkScript)
}

func TestValidateRenewNodeTimelocks(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	handler := createTestRenewLeafHandler()

	// Create test data
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng, tx)
	tree := createTestRenewTree(t, ctx, ownerPubKey, tx)

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
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng, tx)
	tree := createTestRenewTree(t, ctx, ownerPubKey, tx)

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
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng, tx)
	tree := createTestRenewTree(t, ctx, ownerPubKey, tx)

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
