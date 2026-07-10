package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

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

// These tests exercise the consensus.FlowHandler contract of the swap flow —
// the boundary the 2PC engine dispatches to over ConsensusPrepare / commit and
// rollback gossip. Entity fixtures mirror what a real Prepare persists, the
// same pattern as the refund flow handler suite; full multi-SO behavior is
// covered by the grpc_test integration tests.

// TestStaticDepositUtxoSwapFlowHandler_TransferSemantics pins the embedded
// send-transfer handler's wiring: the nested SSP→user transfer must be typed
// TransferTypeUtxoSwap, attributed TransferPartnerTypeDeposit (the legacy
// startTransferInternal switch's attribution for utxo swaps — see #7681 for
// what a silently-wrong partner type costs), and must not require direct
// refund txs (the legacy wire contract passes requireDirectTx=false). The
// coordinator flow is built directly on this same handler
// (buildStaticDepositUtxoSwapCoordinatorFlow), so this pins both sides.
// SaveTransferPartner itself is knob+JWT gated and exercised end-to-end by
// partner_transfer_test.go for the send-transfer flow this delegates to.
func TestStaticDepositUtxoSwapFlowHandler_TransferSemantics(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	assert.Equal(t, st.TransferTypeUtxoSwap, handler.transfer.transferType)
	assert.Equal(t, st.TransferPartnerTypeDeposit, handler.transfer.partnerType)
	assert.False(t, handler.transfer.requireDirectRefunds, "utxo swap packages must not require direct refund txs")
}

// TestStaticDepositSwapJobID_Deterministic pins that the spend-tx signing job
// id is a pure function of (txid, vout), and that it can never collide with the
// refund flow's job id for the same utxo — the two flows share the utxo keying
// but must aggregate independently.
func TestStaticDepositSwapJobID_Deterministic(t *testing.T) {
	t.Parallel()
	txid := []byte{0x01, 0x02, 0x03}
	first := staticDepositSwapJobID(txid, 0)
	second := staticDepositSwapJobID(txid, 0)
	assert.Equal(t, first, second, "same (txid, vout) must derive the same job id")
	assert.NotEqual(t, first, staticDepositSwapJobID(txid, 1), "different vout must derive a different job id")
	assert.NotEqual(t, first, staticDepositSwapJobID([]byte{0x09}, 0), "different txid must derive a different job id")
	assert.NotEqual(t, first, staticDepositRefundJobID(txid, 0), "swap and refund job ids for the same utxo must not collide")
}

// createTestFixedUtxoSwap builds the entity graph the swap Commit / Rollback
// exercise — the state a real Prepare leaves behind: a static deposit address,
// a confirmed Utxo, a Transfer in the given status, and a FIXED_AMOUNT UtxoSwap
// in the given status wired to both.
func createTestFixedUtxoSwap(t *testing.T, ctx context.Context, swapStatus st.UtxoSwapStatus, transferStatus st.TransferStatus) (*ent.UtxoSwap, *ent.Utxo, *ent.Transfer) {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{3})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	coordinatorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	depositAddress := createTestStaticDepositAddress(t, ctx, client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, client, depositAddress, 100)
	// GetTransferFromUtxoSwap cross-checks the transfer's total value against
	// the swap's credit amount, so the fixture must keep them equal.
	transfer := createTestUtxoSwapTransfer(t, ctx, sspIdentityPubKey, ownerIdentityPubKey, client, transferStatus, utxo.Amount)

	utxoSwap, err := client.UtxoSwap.Create().
		SetStatus(swapStatus).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetCreditAmountSats(utxo.Amount).
		SetSspSignature([]byte("test_ssp_quote_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserSignature([]byte("test_user_signature")).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey).
		SetRequestedTransferID(transfer.ID).
		SetTransfer(transfer).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, addUtxoSwapToDepositAddress(ctx, client, depositAddress.ID, utxoSwap))
	return utxoSwap, utxo, transfer
}

// createTestUtxoSwapTransfer mirrors createTestTransfer but with the
// TransferTypeUtxoSwap type — GetTransferFromUtxoSwap (used by both
// CompleteUtxoSwap and CancelUtxoSwap's SP-3261 guard) rejects a transfer whose
// type doesn't match the swap's request type.
func createTestUtxoSwapTransfer(t *testing.T, ctx context.Context, senderPubKey, receiverPubKey keys.Public, client *ent.Client, status st.TransferStatus, totalValue uint64) *ent.Transfer {
	t.Helper()
	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(status).
		SetType(st.TransferTypeUtxoSwap).
		SetSenderIdentityPubkey(senderPubKey).
		SetReceiverIdentityPubkey(receiverPubKey).
		SetTotalValue(totalValue).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	return transfer
}

func swapCommitOpFor(u *ent.Utxo, transfer *ent.Transfer) *pbinternal.StaticDepositUtxoSwapCommitRequest {
	return &pbinternal.StaticDepositUtxoSwapCommitRequest{
		OnChainUtxo:    utxoProtoFor(u),
		TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: transfer.ID.String()},
	}
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_Created_Cancels covers the
// in-flight (pre-commit) case: the transfer is returned first (satisfying
// CancelUtxoSwap's SP-3261 sent-transfer guard), then the CREATED swap is
// cancelled.
func TestStaticDepositUtxoSwapFlowHandler_Rollback_Created_Cancels(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, transfer := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCreated, st.TransferStatusSenderKeyTweakPending)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedSwap.Status)
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status, "transfer must be returned by the rollback")
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_TransferNeverCreated_Cancels
// covers a Prepare that failed before the transfer delegation: the rollback's
// transfer leg is a no-op (NotFound) and the swap still cancels — CancelUtxoSwap
// treats a missing transfer as nothing-claimable-to-orphan.
func TestStaticDepositUtxoSwapFlowHandler_Rollback_TransferNeverCreated_Cancels(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, transfer := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCreated, st.TransferStatusSenderKeyTweakPending)
	// Point the rollback at a transfer id that was never created on this SO.
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	_, err = dbTx.UtxoSwap.UpdateOne(swap).ClearTransfer().Save(ctx)
	require.NoError(t, err)
	require.NoError(t, dbTx.Transfer.DeleteOne(transfer).Exec(ctx))

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedSwap.Status)
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_SentTransfer_FailsClosed pins
// the SP-3261 invariant: a CREATED swap whose transfer is already sent
// (SENDER_KEY_TWEAKED — receiver-claimable) must never be cancelled. This
// combination is an invariant violation under 2PC (tweaks apply only in
// Commit), so Rollback surfaces an error and the reconciler keeps the flow
// visible instead of silently orphaning a claimable transfer.
func TestStaticDepositUtxoSwapFlowHandler_Rollback_SentTransfer_FailsClosed(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, transfer := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCreated, st.TransferStatusSenderKeyTweaked)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Rollback(ctx, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: utxoProtoFor(utxo)})
	require.ErrorContains(t, err, "refusing to cancel")

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updatedSwap.Status, "swap with a sent transfer must not be cancelled")
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status, "sent transfer must not be returned")
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_Completed_NoOp: a
// stray/redelivered rollback after Commit completed the swap must not cancel it
// and must return nil (not an error that loops the reconciler).
func TestStaticDepositUtxoSwapFlowHandler_Rollback_Completed_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, _ := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCompleted, st.TransferStatusSenderKeyTweaked)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updated.Status, "completed swap must not be cancelled")
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_Cancelled_NoOp: a redelivered
// rollback after the swap is already CANCELLED is a no-op.
func TestStaticDepositUtxoSwapFlowHandler_Rollback_Cancelled_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, _ := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCancelled, st.TransferStatusReturned)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updated.Status)
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_AcceptsPrepareOp covers the
// reconciler echo-back path (prepare op rather than the canonical rollback op).
func TestStaticDepositUtxoSwapFlowHandler_Rollback_AcceptsPrepareOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, transfer := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCreated, st.TransferStatusSenderKeyTweakPending)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	prepareOp := &pbinternal.StaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.InitiateStaticDepositUtxoSwapRequest{
			OnChainUtxo: utxoProtoFor(utxo),
			Transfer:    &pbspark.StartTransferRequest{TransferId: transfer.ID.String()},
		},
	}
	require.NoError(t, handler.Rollback(ctx, prepareOp))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedSwap.Status)
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status)
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_NoSwap_NoOp covers the case
// where Prepare never created a swap on this SO (utxo absent).
func TestStaticDepositUtxoSwapFlowHandler_Rollback_NoSwap_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	utxo := &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST}
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: utxo}))
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_RejectsMissingUtxo rejects a
// rollback payload with no utxo rather than panicking.
func TestStaticDepositUtxoSwapFlowHandler_Rollback_RejectsMissingUtxo(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NotPanics(t, func() {
		err := handler.Rollback(t.Context(), &pbinternal.StaticDepositUtxoSwapRollbackRequest{})
		require.ErrorContains(t, err, "on_chain_utxo is required")
	})
}

// TestStaticDepositUtxoSwapFlowHandler_Rollback_RejectsUnexpectedOpType rejects
// an op of the wrong proto type.
func TestStaticDepositUtxoSwapFlowHandler_Rollback_RejectsUnexpectedOpType(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NotPanics(t, func() {
		err := handler.Rollback(t.Context(), &pbinternal.StaticDepositUtxoSwapCommitRequest{})
		require.ErrorContains(t, err, "unexpected operation type")
	})
}

// TestStaticDepositUtxoSwapFlowHandler_Commit_Created_Completes covers the
// happy participant commit. The transfer is already SENDER_KEY_TWEAKED (the
// state a real commit leaves it in — here applySendTransferCommit
// short-circuits as an idempotent retry), which satisfies CompleteUtxoSwap's
// sent-transfer requirement; the CREATED swap is marked COMPLETED.
func TestStaticDepositUtxoSwapFlowHandler_Commit_Created_Completes(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, transfer := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCreated, st.TransferStatusSenderKeyTweaked)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Commit(ctx, swapCommitOpFor(utxo, transfer)))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updated.Status)
}

// TestStaticDepositUtxoSwapFlowHandler_Commit_UnsentTransfer_ReturnsError pins
// the commit-ordering dependency: CompleteUtxoSwap on a non-refund swap
// requires the transfer to be sent. A transfer stuck pre-commit (here:
// SenderInitiated, which applySendTransferCommit also rejects as an invariant
// violation) must fail the commit rather than complete a swap whose transfer
// never reached the receiver.
func TestStaticDepositUtxoSwapFlowHandler_Commit_UnsentTransfer_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, transfer := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCreated, st.TransferStatusSenderInitiated)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Commit(ctx, swapCommitOpFor(utxo, transfer))
	require.Error(t, err)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updated.Status, "swap must not complete when its transfer is unsent")
}

// TestStaticDepositUtxoSwapFlowHandler_Commit_Idempotent covers gossip
// redelivery: a second Commit against an already-COMPLETED swap (and
// already-tweaked transfer) is a no-op.
func TestStaticDepositUtxoSwapFlowHandler_Commit_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, transfer := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCompleted, st.TransferStatusSenderKeyTweaked)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Commit(ctx, swapCommitOpFor(utxo, transfer)))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updated.Status)
}

// TestStaticDepositUtxoSwapFlowHandler_Commit_NoSwap_ReturnsError pins the
// deliberate asymmetry with Rollback: a Commit for a utxo with no active swap
// keeps erroring (and retrying via the engine) rather than silently no-op'ing.
func TestStaticDepositUtxoSwapFlowHandler_Commit_NoSwap_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{4})
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	// Post-tweak transfer: applySendTransferCommit short-circuits (idempotent
	// retry), then completeFixedSwap errors on the missing swap.
	transfer := createTestTransfer(t, ctx, rng, dbTx, st.TransferStatusSenderKeyTweaked)

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	utxo := &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST}
	err = handler.Commit(ctx, &pbinternal.StaticDepositUtxoSwapCommitRequest{
		OnChainUtxo:    utxo,
		TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: transfer.ID.String()},
	})
	require.Error(t, err, "Commit with no active swap must return an error, not no-op")
}

// TestStaticDepositUtxoSwapFlowHandler_Commit_PreCommitTransferNoSwap_ReturnsError
// covers the first-time (not-yet-committed) leg the post-tweak test above does
// not: a transfer still in the pre-commit SenderKeyTweakPending state with no
// active swap row (e.g. cancelled mid-flight by a stray legacy rollback). Commit
// must return an error rather than silently no-op — that error is what makes the
// enclosing DB transaction roll back the sender-key-tweak applySendTransferCommit
// performed, so no "cancelled swap + tweaked leaf" state can persist. Guards
// against a future change that swallows the missing-swap error into a no-op.
func TestStaticDepositUtxoSwapFlowHandler_Commit_PreCommitTransferNoSwap_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo, transfer := createTestFixedUtxoSwap(t, ctx, st.UtxoSwapStatusCreated, st.TransferStatusSenderKeyTweakPending)

	// Cancel the swap out from under the flow, leaving the transfer pre-commit —
	// the exact mid-flight state the reviewer flagged. loadFixedSwapForUtxo
	// filters to CREATED/COMPLETED, so a CANCELLED row is invisible (no active swap).
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, CancelUtxoSwap(ctx, swap))

	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err = handler.Commit(ctx, swapCommitOpFor(utxo, transfer))
	require.Error(t, err, "Commit must error when the swap was cancelled mid-flight, so the tx (incl. the key tweak) rolls back")

	// The transfer must not have been left COMPLETED/finalized by the partial apply.
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.NotEqual(t, st.TransferStatusCompleted, updatedTransfer.Status)
}

// TestStaticDepositUtxoSwapFlowHandler_Commit_RejectsMissingTransferCommit: the
// commit payload without the embedded transfer commit is malformed — applying
// the swap completion without the transfer commit would break the ordering
// invariant.
func TestStaticDepositUtxoSwapFlowHandler_Commit_RejectsMissingTransferCommit(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Commit(t.Context(), &pbinternal.StaticDepositUtxoSwapCommitRequest{
		OnChainUtxo: &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST},
	})
	require.ErrorContains(t, err, "transfer_commit is required")
}

// TestStaticDepositUtxoSwapFlowHandler_Prepare_RejectsMissingFields upholds the
// 2PC invariant "Prepare success => commit-side success (modulo infra)": the
// spend-tx nonce commitment is parsed by BuildCommitPayload, so Prepare must
// reject it (and the other required fields) up front. All checks run before any
// DB access, so no entity graph is needed.
func TestStaticDepositUtxoSwapFlowHandler_Prepare_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	utxo := &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST}
	transfer := &pbspark.StartTransferRequest{TransferPackage: &pbspark.TransferPackage{}}

	cases := []struct {
		name    string
		req     *pbinternal.InitiateStaticDepositUtxoSwapRequest
		wantErr string
	}{
		{
			name:    "missing utxo",
			req:     &pbinternal.InitiateStaticDepositUtxoSwapRequest{Transfer: transfer},
			wantErr: "on_chain_utxo is required",
		},
		{
			name:    "missing transfer",
			req:     &pbinternal.InitiateStaticDepositUtxoSwapRequest{OnChainUtxo: utxo},
			wantErr: "transfer is required",
		},
		{
			name:    "missing transfer package",
			req:     &pbinternal.InitiateStaticDepositUtxoSwapRequest{OnChainUtxo: utxo, Transfer: &pbspark.StartTransferRequest{}},
			wantErr: "transfer_package is required",
		},
		{
			name:    "missing spend tx signing job",
			req:     &pbinternal.InitiateStaticDepositUtxoSwapRequest{OnChainUtxo: utxo, Transfer: transfer},
			wantErr: "spend_tx_signing_job is required",
		},
		{
			name: "missing spend tx nonce commitment",
			req: &pbinternal.InitiateStaticDepositUtxoSwapRequest{
				OnChainUtxo:       utxo,
				Transfer:          transfer,
				SpendTxSigningJob: &pbspark.SigningJob{RawTx: []byte{0x01}},
			},
			wantErr: "signing_nonce_commitment is required",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handler.Prepare(t.Context(), &pbinternal.StaticDepositUtxoSwapPrepareRequest{OriginalRequest: tt.req})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestStaticDepositUtxoSwapFlowHandler_Prepare_RejectsUnexpectedOpType rejects
// an op of the wrong proto type.
func TestStaticDepositUtxoSwapFlowHandler_Prepare_RejectsUnexpectedOpType(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoSwapFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	_, err := handler.Prepare(t.Context(), &pbinternal.StaticDepositUtxoSwapCommitRequest{})
	require.ErrorContains(t, err, "unexpected operation type")
}
