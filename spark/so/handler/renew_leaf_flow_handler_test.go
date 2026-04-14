package handler

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestNodeForFlowHandler(t *testing.T, ctx context.Context, status st.TreeNodeStatus) *ent.TreeNode {
	t.Helper()
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.Reader
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"test": secretShare.Public()}).
		SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	baseTxid := st.NewRandomTxIDForTesting(t)
	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(identityPubKey).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(baseTxid).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	node, err := tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetStatus(status).
		SetOwnerIdentityPubkey(identityPubKey).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(keyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)
	return node
}

func TestRenewLeafFlowHandler_Prepare_RejectsNonAvailable(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	node := createTestNodeForFlowHandler(t, ctx, st.TreeNodeStatusTransferLocked)

	handler := NewRenewLeafFlowHandler(nil)
	req := &pbspark.RenewLeafRequest{LeafId: node.ID.String()}
	_, err := handler.Prepare(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected Available")
}

func TestRenewLeafFlowHandler_Rollback_ResetsToAvailable(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	node := createTestNodeForFlowHandler(t, ctx, st.TreeNodeStatusRenewLocked)

	handler := NewRenewLeafFlowHandler(nil)
	req := &pbspark.RenewLeafRequest{LeafId: node.ID.String()}
	err := handler.Rollback(ctx, req)
	require.NoError(t, err)

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := tx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAvailable, updated.Status)
}

func TestRenewLeafFlowHandler_Rollback_IdempotentOnAvailable(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	node := createTestNodeForFlowHandler(t, ctx, st.TreeNodeStatusAvailable)

	handler := NewRenewLeafFlowHandler(nil)
	req := &pbspark.RenewLeafRequest{LeafId: node.ID.String()}
	err := handler.Rollback(ctx, req)
	require.NoError(t, err)

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := tx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAvailable, updated.Status)
}

func TestRenewLeafFlowHandler_Rollback_NonExistentNode(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewRenewLeafFlowHandler(nil)
	req := &pbspark.RenewLeafRequest{LeafId: "019ce48c-0000-7000-0000-000000000000"}
	err := handler.Rollback(ctx, req)
	require.NoError(t, err)
}
