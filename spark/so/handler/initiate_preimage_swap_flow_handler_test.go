package handler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsPreimageSwapSettleableStatus pins the pre-commit status set in which the
// Commit handler applies refund signatures / settles sender key tweaks. It
// enumerates every TransferStatus so adding one to the enum forces a deliberate
// classification here rather than silently landing in the non-settleable bucket
// (where Commit would skip settlement) or the settleable one (where a replayed
// Commit could re-run settlement against advanced state). Mirrors the
// change-detector guard on isProvidePreimageCommittableStatus.
func TestIsPreimageSwapSettleableStatus(t *testing.T) {
	settleable := map[st.TransferStatus]bool{
		st.TransferStatusSenderInitiatedCoordinator: true,
		st.TransferStatusSenderKeyTweakPending:      true,
		st.TransferStatusApplyingSenderKeyTweak:     true,

		st.TransferStatusSenderInitiated:         false,
		st.TransferStatusSenderKeyTweaked:        false,
		st.TransferStatusReceiverKeyTweaked:      false,
		st.TransferStatusReceiverKeyTweakLocked:  false,
		st.TransferStatusReceiverKeyTweakApplied: false,
		st.TransferStatusReceiverRefundSigned:    false,
		st.TransferStatusCompleted:               false,
		st.TransferStatusExpired:                 false,
		st.TransferStatusReturned:                false,
	}

	// Guard: the table must cover the full enum. If a new status is added to
	// TransferStatus.Values() without a row here, this fails.
	var enumValues st.TransferStatus
	for _, v := range enumValues.Values() {
		status := st.TransferStatus(v)
		if _, ok := settleable[status]; !ok {
			t.Fatalf("TransferStatus %q is not covered by this test — add it to the settleable map with the intended classification", v)
		}
	}

	for status, want := range settleable {
		assert.Equalf(t, want, isPreimageSwapSettleableStatus(status), "isPreimageSwapSettleableStatus(%s)", status)
	}
}

// createTestPreimageSwapTransfer builds the minimal entity graph a preimage-swap
// rollback exercises: a PreimageSwap-type Transfer in the given status with a
// single TransferLocked leaf wired through a TransferLeaf.
func createTestPreimageSwapTransfer(t *testing.T, ctx context.Context, status st.TransferStatus) (*ent.Transfer, *ent.TreeNode) {
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
		SetType(st.TransferTypePreimageSwap).
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

	// A preimage swap always has exactly one linked PreimageRequest (Prepare
	// creates it via storeUserSignedTransactions); executeCancelTransfer's
	// cancelTransferCancelRequest requires it.
	paymentHash := make([]byte, 32)
	paymentHash[31] = 0x01
	_, err = dbTx.PreimageRequest.Create().
		SetPaymentHash(paymentHash).
		SetReceiverIdentityPubkey(receiverPK).
		SetSenderIdentityPubkey(senderPK).
		SetTransfers(transfer).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		Save(ctx)
	require.NoError(t, err)

	return transfer, leaf
}

// TestInitiatePreimageSwapFlowHandler_Rollback_PreCommit_Returns covers the
// in-flight (pre-commit) case: a transfer Prepare left at SENDER_KEY_TWEAK_PENDING
// (the +transfer-package status) is cancelled — marked RETURNED and its leaf
// unlocked — the same mechanism the legacy cancel path uses.
func TestInitiatePreimageSwapFlowHandler_Rollback_PreCommit_Returns(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	transfer, leaf := createTestPreimageSwapTransfer(t, ctx, st.TransferStatusSenderKeyTweakPending)

	handler := NewInitiatePreimageSwapFlowHandler(nil)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: transfer.ID.String()}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status, "transfer should be RETURNED")

	updatedLeaf, err := dbTx.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAvailable, updatedLeaf.Status, "leaf should be unlocked")
}

// TestInitiatePreimageSwapFlowHandler_Rollback_SenderInitiated_Returns covers the
// no-transfer-package / HODL-receive pre-commit status (SENDER_INITIATED, created
// without a key tweak map) — it must also cancel cleanly.
func TestInitiatePreimageSwapFlowHandler_Rollback_SenderInitiated_Returns(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	transfer, leaf := createTestPreimageSwapTransfer(t, ctx, st.TransferStatusSenderInitiated)

	handler := NewInitiatePreimageSwapFlowHandler(nil)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: transfer.ID.String()}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status)
	updatedLeaf, err := dbTx.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAvailable, updatedLeaf.Status)
}

// TestInitiatePreimageSwapFlowHandler_Rollback_Idempotent_AlreadyReturned covers
// gossip redelivery: a second rollback against an already-RETURNED transfer is a
// no-op.
func TestInitiatePreimageSwapFlowHandler_Rollback_Idempotent_AlreadyReturned(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	transfer, _ := createTestPreimageSwapTransfer(t, ctx, st.TransferStatusReturned)

	handler := NewInitiatePreimageSwapFlowHandler(nil)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: transfer.ID.String()}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status)
}

// TestInitiatePreimageSwapFlowHandler_Rollback_PostCommit_NoOp is the
// safety-critical case: a stray/redelivered rollback arriving after Commit
// settled the sender key tweaks (non-HODL receive → SENDER_KEY_TWEAKED) must NOT
// cancel the committed transfer, and must return nil rather than a plain error
// (which would loop the reconciler in runConsensusRollback). The transfer and its
// leaf lock must stay intact.
func TestInitiatePreimageSwapFlowHandler_Rollback_PostCommit_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	transfer, leaf := createTestPreimageSwapTransfer(t, ctx, st.TransferStatusSenderKeyTweaked)

	handler := NewInitiatePreimageSwapFlowHandler(nil)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: transfer.ID.String()}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status, "committed transfer must not be rolled back")
	updatedLeaf, err := dbTx.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusTransferLocked, updatedLeaf.Status, "leaf must stay locked")
}

// TestInitiatePreimageSwapFlowHandler_Rollback_AcceptsPrepareOp verifies the
// reconciler echo-back path: a rollback dispatched with the prepare op rather
// than the canonical rollback payload still resolves the transfer via the nested
// request.transfer.transfer_id.
func TestInitiatePreimageSwapFlowHandler_Rollback_AcceptsPrepareOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	transfer, _ := createTestPreimageSwapTransfer(t, ctx, st.TransferStatusSenderKeyTweakPending)

	handler := NewInitiatePreimageSwapFlowHandler(nil)
	prepareOp := &pbinternal.InitiatePreimageSwapPrepareRequest{
		OriginalRequest: &pbspark.InitiatePreimageSwapRequest{
			Transfer: &pbspark.StartUserSignedTransferRequest{TransferId: transfer.ID.String()},
		},
	}
	require.NoError(t, handler.Rollback(ctx, prepareOp))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status)
}

// TestInitiatePreimageSwapFlowHandler_Rollback_NonExistent is a no-op when
// Prepare never created the transfer on this SO.
func TestInitiatePreimageSwapFlowHandler_Rollback_NonExistent(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewInitiatePreimageSwapFlowHandler(nil)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: uuid.New().String()}))
}

// TestInitiatePreimageSwapFlowHandler_Rollback_RejectsMissingTransferID rejects a
// rollback payload with no transfer id rather than panicking.
func TestInitiatePreimageSwapFlowHandler_Rollback_RejectsMissingTransferID(t *testing.T) {
	t.Parallel()

	handler := NewInitiatePreimageSwapFlowHandler(nil)
	require.NotPanics(t, func() {
		err := handler.Rollback(t.Context(), &pbinternal.InitiatePreimageSwapRollbackRequest{})
		require.ErrorContains(t, err, "transfer_id is required")
	})
}

// TestInitiatePreimageSwapFlowHandler_Rollback_RejectsUnexpectedOpType rejects an
// op of the wrong proto type rather than panicking.
func TestInitiatePreimageSwapFlowHandler_Rollback_RejectsUnexpectedOpType(t *testing.T) {
	t.Parallel()

	handler := NewInitiatePreimageSwapFlowHandler(nil)
	require.NotPanics(t, func() {
		err := handler.Rollback(t.Context(), &pbinternal.InitiatePreimageSwapCommitRequest{})
		require.ErrorContains(t, err, "unexpected operation type")
	})
}
