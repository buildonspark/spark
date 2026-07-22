package handler

import (
	"context"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/cooperativeexit"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestCoopExitTransfer builds the minimal entity graph a coop-exit
// rollback exercises: a CooperativeExit-type Transfer in the given status, a
// single TransferLocked leaf wired through a TransferLeaf, and a cooperative_exit
// row. It returns the transfer and its leaf so callers can assert on both.
func createTestCoopExitTransfer(t *testing.T, ctx context.Context, status st.TransferStatus) (*ent.Transfer, *ent.TreeNode) {
	t.Helper()
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	leaf := createTestNodeForFlowHandler(t, ctx, st.TreeNodeStatusTransferLocked)
	senderPK := keys.GeneratePrivateKey().Public()
	receiverPK := keys.GeneratePrivateKey().Public()

	transfer, err := dbTx.Transfer.Create().
		SetSenderIdentityPubkey(senderPK).
		SetReceiverIdentityPubkey(receiverPK).
		SetStatus(status).
		SetType(st.TransferTypeCooperativeExit).
		SetTotalValue(leaf.Value).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)

	sender, err := createTransferSender(ctx, dbTx, transfer, senderPK)
	require.NoError(t, err)
	receiver, err := createTransferReceiver(ctx, dbTx, transfer, receiverPK, st.TransferReceiverStatusInitiated)
	require.NoError(t, err)

	refundTx := createOldBitcoinTxBytes(t, receiverPK)
	require.NoError(t, createTransferLeaves(
		ctx, dbTx, transfer, sender, receiver, []*ent.TreeNode{leaf},
		map[string][]byte{leaf.ID.String(): refundTx},
		nil, nil, nil,
	))

	_, err = dbTx.CooperativeExit.Create().
		SetID(uuid.New()).
		SetTransfer(transfer).
		SetExitTxid(st.NewRandomTxIDForTesting(t)).
		Save(ctx)
	require.NoError(t, err)

	return transfer, leaf
}

func coopExitRow(t *testing.T, ctx context.Context, transferID uuid.UUID) int {
	t.Helper()
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	count, err := dbTx.CooperativeExit.Query().
		Where(cooperativeexit.HasTransferWith(enttransfer.ID(transferID))).
		Count(ctx)
	require.NoError(t, err)
	return count
}

// TestCoopExitFlowHandler_Rollback_PreCommit_ReturnsAndDeletes covers the
// in-flight (pre-commit) case: a transfer Prepare left at SENDER_INITIATED is
// cancelled — marked RETURNED, its leaf unlocked, and its cooperative_exit row
// deleted so the chain watcher no longer considers the exit pending.
func TestCoopExitFlowHandler_Rollback_PreCommit_ReturnsAndDeletes(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	transfer, leaf := createTestCoopExitTransfer(t, ctx, st.TransferStatusSenderInitiated)

	handler := NewCoopExitFlowHandler(nil)
	err := handler.Rollback(ctx, &pbinternal.CoopExitRollbackRequest{TransferId: transfer.ID.String()})
	require.NoError(t, err)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status, "transfer should be RETURNED")

	updatedLeaf, err := dbTx.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAvailable, updatedLeaf.Status, "leaf should be unlocked")

	assert.Equal(t, 0, coopExitRow(t, ctx, transfer.ID), "cooperative_exit row should be deleted")
}

// TestCoopExitFlowHandler_Rollback_PostCommit_NoOp is the safety-critical case:
// a stale/duplicate rollback arriving after Commit promoted the transfer to
// SENDER_KEY_TWEAK_PENDING must NOT cancel the committed exit (executeCancelTransfer
// would otherwise accept that status and unlock leaves the SSP is about to
// claim). The transfer, leaf lock, and cooperative_exit row must all be intact.
func TestCoopExitFlowHandler_Rollback_PostCommit_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	transfer, leaf := createTestCoopExitTransfer(t, ctx, st.TransferStatusSenderKeyTweakPending)

	handler := NewCoopExitFlowHandler(nil)
	err := handler.Rollback(ctx, &pbinternal.CoopExitRollbackRequest{TransferId: transfer.ID.String()})
	require.NoError(t, err)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, updatedTransfer.Status,
		"committed transfer must not be rolled back")

	updatedLeaf, err := dbTx.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusTransferLocked, updatedLeaf.Status, "leaf must stay locked")

	assert.Equal(t, 1, coopExitRow(t, ctx, transfer.ID), "cooperative_exit row must survive")
}

// TestCoopExitFlowHandler_Rollback_AcceptsPrepareOp verifies the reconciler
// echo-back path: a rollback dispatched with the prepare op (CoopExitPrepareRequest)
// rather than the canonical rollback payload still resolves the transfer.
func TestCoopExitFlowHandler_Rollback_AcceptsPrepareOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	transfer, _ := createTestCoopExitTransfer(t, ctx, st.TransferStatusSenderInitiated)

	handler := NewCoopExitFlowHandler(nil)
	prepareOp := &pbinternal.CoopExitPrepareRequest{
		OriginalRequest: &pb.CooperativeExitRequest{
			Transfer: &pb.StartTransferRequest{TransferId: transfer.ID.String()},
		},
	}
	require.NoError(t, handler.Rollback(ctx, prepareOp))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status)
}

// TestCoopExitFlowHandler_Rollback_NonExistent is a no-op when Prepare never
// created the transfer on this SO.
func TestCoopExitFlowHandler_Rollback_NonExistent(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewCoopExitFlowHandler(nil)
	req := &pbinternal.CoopExitRollbackRequest{TransferId: uuid.New().String()}
	require.NoError(t, handler.Rollback(ctx, req))
}

// TestCoopExitFlowHandler_Rollback_RejectsMissingTransferID rejects a rollback
// payload with no transfer id rather than panicking.
func TestCoopExitFlowHandler_Rollback_RejectsMissingTransferID(t *testing.T) {
	t.Parallel()

	handler := NewCoopExitFlowHandler(nil)
	require.NotPanics(t, func() {
		err := handler.Rollback(t.Context(), &pbinternal.CoopExitRollbackRequest{})
		require.ErrorContains(t, err, "transfer_id is required")
	})
}

// TestCoopExitFlowHandler_Prepare_BindingValidationPrecedesPackageParsing pins
// the participant-side validation ordering: a request whose exit_txid fails the
// connector-tx binding AND whose transfer package is invalid must be rejected
// by the binding validator, matching the coordinator fast-fail. The
// coordinator rejects such a
// request before engine fan-out, so the end-to-end coop-exit test never drives
// this combination through Prepare — this unit test covers the participant
// seam directly. The binding check runs on raw request fields before any DB or
// config access, so a nil config and bare context suffice; a package-parsing
// error surfacing here means Prepare consulted the package first.
func TestCoopExitFlowHandler_Prepare_BindingValidationPrecedesPackageParsing(t *testing.T) {
	t.Parallel()

	var legitParent chainhash.Hash
	for i := range legitParent {
		legitParent[i] = byte(i + 1)
	}
	connectorTx := wire.NewMsgTx(3)
	connectorTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: legitParent, Index: 1}})
	connectorTx.AddTxOut(&wire.TxOut{Value: 354, PkScript: []byte{0x51}})
	connectorTxBytes, err := common.SerializeTx(connectorTx)
	require.NoError(t, err)

	// Alibi exit_txid: 32 bytes that do not match legitParent in either endian.
	alibiExitTxid := make([]byte, 32)
	for i := range alibiExitTxid {
		alibiExitTxid[i] = byte(255 - i)
	}

	handler := NewCoopExitFlowHandler(nil)
	_, err = handler.Prepare(t.Context(), &pbinternal.CoopExitPrepareRequest{
		OriginalRequest: &pb.CooperativeExitRequest{
			Transfer: &pb.StartTransferRequest{
				TransferId:      uuid.NewString(),
				TransferPackage: &pb.TransferPackage{}, // invalid: empty key tweak package
			},
			ExitId:      uuid.NewString(),
			ExitTxid:    alibiExitTxid,
			ConnectorTx: connectorTxBytes,
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "does not match exit_txid",
		"rejection must come from the connector-binding validator, not package parsing")
}
