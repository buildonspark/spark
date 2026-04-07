package handler

import (
	"testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// makeMinimalRawTx returns a serialized Bitcoin transaction suitable for DB
// schema hooks that parse raw_tx to extract TXIDs.
func makeMinimalRawTx(t *testing.T) []byte {
	t.Helper()
	srcScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)
	tx := newTestTx(testSourceValue, 0, nil, srcScript)
	return serializeTx(t, tx)
}

// TestMarkTreesExited_RejectsTransferLockedNode verifies that MarkTreesExited
// refuses to overwrite a leaf whose status is TransferLocked.  This prevents a
// TOCTOU race where an in-flight transfer could be silently corrupted by a
// concurrent tree-exit operation.
func TestMarkTreesExited_RejectsTransferLockedNode(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rawTx := makeMinimalRawTx(t)
	rawRefundTx := makeMinimalRawTx(t)

	// Create a minimal tree in Available status.
	tree, err := dbTx.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		Save(ctx)
	require.NoError(t, err)

	// Create a signing keyshare for the node.
	secret := keys.GeneratePrivateKey()
	ks, err := dbTx.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"1": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create a leaf in TransferLocked status to simulate an in-flight transfer.
	node, err := dbTx.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(ks).
		SetValue(testSourceValue).
		SetVerifyingPubkey(secret.Public()).
		SetOwnerIdentityPubkey(secret.Public()).
		SetOwnerSigningPubkey(secret.Public()).
		SetVout(0).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetRawTx(rawTx).
		SetRawRefundTx(rawRefundTx).
		Save(ctx)
	require.NoError(t, err)

	handler := newTreeExitHandler(&so.Config{})
	err = handler.markTreesExited(ctx, []*ent.Tree{tree})
	require.Error(t, err)
	require.Contains(t, err.Error(), "locked status")

	// Verify the node status was NOT changed to Exited.
	refreshed, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, refreshed.Status,
		"TransferLocked node must not be overwritten by MarkTreesExited")
}

// TestMarkTreesExited_SucceedsForAvailableNodes verifies the happy path:
// trees with only Available nodes can be marked as exited.
func TestMarkTreesExited_SucceedsForAvailableNodes(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rawTx := makeMinimalRawTx(t)
	rawRefundTx := makeMinimalRawTx(t)

	tree, err := dbTx.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		Save(ctx)
	require.NoError(t, err)

	secret := keys.GeneratePrivateKey()
	ks, err := dbTx.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"1": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbTx.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(ks).
		SetValue(testSourceValue).
		SetVerifyingPubkey(secret.Public()).
		SetOwnerIdentityPubkey(secret.Public()).
		SetOwnerSigningPubkey(secret.Public()).
		SetVout(0).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeNodeStatusAvailable).
		SetRawTx(rawTx).
		SetRawRefundTx(rawRefundTx).
		Save(ctx)
	require.NoError(t, err)

	handler := newTreeExitHandler(&so.Config{})
	err = handler.markTreesExited(ctx, []*ent.Tree{tree})
	require.NoError(t, err)
}
