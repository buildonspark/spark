package handler

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the consensus.FlowHandler contract of the instant
// reserve flow — the boundary the 2PC engine dispatches to over
// ConsensusPrepare / commit and rollback gossip. Entity fixtures mirror what a
// real Prepare persists (an INSTANT swap in CREATED with a linked transfer, no
// Utxo edge); full multi-SO behavior is covered by the grpc_test integration
// tests.

// createTestInstantReserveSwap builds the reserved state a real Prepare leaves
// behind: a static deposit address, an INSTANT UtxoSwap in CREATED with NO Utxo
// edge (the instant flow does not require a confirmed UTXO at reserve), and a
// UTXO_SWAP-typed transfer linked to it.
func createTestInstantReserveSwap(t *testing.T, ctx context.Context, transferStatus st.TransferStatus) (*ent.UtxoSwap, *ent.Transfer, keys.Public) {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{7})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	coordinatorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	depositAddress := createTestStaticDepositAddress(t, ctx, client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	transfer := createTestUtxoSwapTransfer(t, ctx, sspIdentityPubKey, ownerIdentityPubKey, client, transferStatus, 90_000)

	utxoSwap, err := client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(100_000).
		SetCreditAmountSats(90_000).
		SetSspSignature([]byte("test_ssp_quote_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserSignature([]byte("test_user_signature")).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey).
		SetRequestedTransferID(transfer.ID).
		SetTransfer(transfer).
		SetConsensusManaged(true).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, addUtxoSwapToDepositAddress(ctx, client, depositAddress.ID, utxoSwap))
	return utxoSwap, transfer, coordinatorPubKey
}

func instantRollbackOpFor(transfer *ent.Transfer) *pbinternal.ReserveInstantStaticDepositUtxoSwapRollbackRequest {
	return &pbinternal.ReserveInstantStaticDepositUtxoSwapRollbackRequest{RequestedTransferId: transfer.ID.String()}
}

// scopedRollbackCtx mimics production, where dispatchConsensusRollback attaches
// the flow's coordinator identity (from the participant FlowExecution row) to
// ctx before the handler runs. A RollbackRequest fails closed without it.
func scopedRollbackCtx(ctx context.Context, coordinatorPubKey keys.Public) context.Context {
	return consensus.WithCoordinatorIdentity(ctx, coordinatorPubKey)
}

// TestReserveInstantFlowHandler_Rollback_Created_Cancels covers the in-flight
// (pre-completion) case: the transfer is returned first (satisfying
// CancelUtxoSwap's SP-3261 guard), then the CREATED instant swap is cancelled.
func TestReserveInstantFlowHandler_Rollback_Created_Cancels(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, coordinatorPubKey := createTestInstantReserveSwap(t, ctx, st.TransferStatusSenderKeyTweakPending)

	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(scopedRollbackCtx(ctx, coordinatorPubKey), instantRollbackOpFor(transfer)))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedSwap.Status)
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedTransfer.Status)
}

// TestReserveInstantFlowHandler_Rollback_AcceptsPrepareOp covers the
// reconciler echo-back path (prepare op rather than the canonical rollback op).
func TestReserveInstantFlowHandler_Rollback_AcceptsPrepareOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, coordinatorPubKey := createTestInstantReserveSwap(t, ctx, st.TransferStatusSenderKeyTweakPending)

	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	prepareOp := &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{
			Transfer: &pbspark.StartTransferRequest{TransferId: transfer.ID.String()},
		},
	}
	require.NoError(t, handler.Rollback(scopedRollbackCtx(ctx, coordinatorPubKey), prepareOp))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedSwap.Status)
}

// TestReserveInstantFlowHandler_Rollback_NoSwap_NoOp covers a rollback for a
// transfer that never reserved a swap on this SO.
func TestReserveInstantFlowHandler_Rollback_NoSwap_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	scopedCtx := scopedRollbackCtx(ctx, keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{55})).Public())
	require.NoError(t, handler.Rollback(scopedCtx, &pbinternal.ReserveInstantStaticDepositUtxoSwapRollbackRequest{
		RequestedTransferId: uuid.NewString(),
	}))
}

// TestReserveInstantFlowHandler_Rollback_Cancelled_NoOp is the idempotency
// leg: the CREATED-only rollback scope means a reservation already CANCELLED
// (or, at claim time, COMPLETED — both non-CREATED) is invisible to a
// redelivered rollback and stays put. Using CANCELLED here because a COMPLETED
// instant swap requires a utxo edge (a DB invariant the claim phase satisfies),
// which the reserve-phase fixture deliberately lacks.
func TestReserveInstantFlowHandler_Rollback_Cancelled_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, coordinatorPubKey := createTestInstantReserveSwap(t, ctx, st.TransferStatusReturned)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, CancelUtxoSwap(ctx, swap))

	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(scopedRollbackCtx(ctx, coordinatorPubKey), instantRollbackOpFor(transfer)))

	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedSwap.Status, "already-cancelled reservation stays cancelled")
}

// TestReserveInstantFlowHandler_Rollback_CommittedReservation_NoOp pins the
// idempotency fix for the reserve phase's defining quirk: a committed
// reservation stays CREATED (not COMPLETED) with a sent transfer, so a
// stale/redelivered rollback still matches the (requested_transfer_id, INSTANT,
// CREATED) query. It must be absorbed as a no-op (the reservation is
// effectively committed and its transfer is claimable) rather than erroring via
// the SP-3261 guard, which runConsensusRollback would loop on.
func TestReserveInstantFlowHandler_Rollback_CommittedReservation_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, coordinatorPubKey := createTestInstantReserveSwap(t, ctx, st.TransferStatusSenderKeyTweaked)

	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(scopedRollbackCtx(ctx, coordinatorPubKey), instantRollbackOpFor(transfer)))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updatedSwap.Status, "committed reservation must stay CREATED, not be cancelled")
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status, "sent transfer must not be returned")
}

// TestReserveInstantFlowHandler_Rollback_CrossCoordinatorScoped verifies the
// coordinator-scoping fence: when a coordinator identity is present in ctx
// (attached by dispatchConsensusRollback from the flow's participant row) and
// it differs from the reservation's stored coordinator, the rollback does NOT
// touch that reservation — a Byzantine coordinator rolling back its own flow
// cannot cancel another SO's in-flight reservation by naming its transfer id.
func TestReserveInstantFlowHandler_Rollback_CrossCoordinatorScoped(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, _ := createTestInstantReserveSwap(t, ctx, st.TransferStatusSenderKeyTweakPending)

	// A different coordinator than the one recorded on the reservation.
	otherCoordinator := keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{99})).Public()
	scopedCtx := scopedRollbackCtx(ctx, otherCoordinator)

	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(scopedCtx, instantRollbackOpFor(transfer)))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updatedSwap.Status, "a different coordinator must not roll back this reservation")
	updatedTransfer, err := dbTx.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.NotEqual(t, st.TransferStatusReturned, updatedTransfer.Status, "transfer must not be returned by a foreign-coordinator rollback")
}

// TestReserveInstantFlowHandler_Rollback_RejectsMissingTransferID rejects a
// rollback payload with no transfer id rather than acting on a zero uuid.
func TestReserveInstantFlowHandler_Rollback_RejectsMissingTransferID(t *testing.T) {
	t.Parallel()
	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Rollback(t.Context(), &pbinternal.ReserveInstantStaticDepositUtxoSwapRollbackRequest{})
	require.ErrorContains(t, err, "requested_transfer_id is required")
}

// TestReserveInstantFlowHandler_Rollback_RejectsUnexpectedOpType rejects an op
// of the wrong proto type.
func TestReserveInstantFlowHandler_Rollback_RejectsUnexpectedOpType(t *testing.T) {
	t.Parallel()
	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Rollback(t.Context(), &pbinternal.ReserveInstantStaticDepositUtxoSwapCommitRequest{})
	require.ErrorContains(t, err, "unexpected operation type")
}

// TestReserveInstantFlowHandler_Rollback_FailsClosedWithoutCoordinatorScope
// pins the fail-closed behavior: a coordinator-authored RollbackRequest with no
// coordinator identity in ctx (unresolvable index, missing FlowExecution row,
// empty flow_execution_id, transient error) must NOT run the lookup unscoped —
// it errors, leaving the reservation untouched so a Byzantine or malformed
// rollback can't cancel a reservation it isn't scoped to.
func TestReserveInstantFlowHandler_Rollback_FailsClosedWithoutCoordinatorScope(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, _ := createTestInstantReserveSwap(t, ctx, st.TransferStatusSenderKeyTweakPending)

	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	// No consensus.WithCoordinatorIdentity on ctx → must fail closed.
	err := handler.Rollback(ctx, instantRollbackOpFor(transfer))
	require.ErrorContains(t, err, "coordinator identity unavailable")

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updatedSwap.Status, "an unscoped rollback must not cancel the reservation")
}

// TestReserveInstantFlowHandler_Rollback_PrepareOpIsCoordinatorScoped pins the
// fix for the reconciler-echo arm: gossip dispatch derives the op's Go type from
// the payload's own Any URL, so a Byzantine coordinator could gossip a
// PrepareRequest naming another honest SO's transfer id under its own in-flight
// flow. The PrepareRequest arm is therefore coordinator-scoped too — when the ctx
// coordinator differs from the reservation's, the lookup misses and the
// reservation is left untouched.
func TestReserveInstantFlowHandler_Rollback_PrepareOpIsCoordinatorScoped(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, _ := createTestInstantReserveSwap(t, ctx, st.TransferStatusSenderKeyTweakPending)

	otherCoordinator := keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{123})).Public()
	prepareOp := &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{
			Transfer: &pbspark.StartTransferRequest{TransferId: transfer.ID.String()},
		},
	}
	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(scopedRollbackCtx(ctx, otherCoordinator), prepareOp))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updatedSwap.Status, "a foreign-coordinator PrepareRequest must not cancel this reservation")
}

// TestReserveInstantFlowHandler_Rollback_PrepareOpFailsClosedWithoutScope pins
// that the PrepareRequest arm, like the RollbackRequest arm, refuses to run the
// lookup unscoped when no coordinator identity is on ctx.
func TestReserveInstantFlowHandler_Rollback_PrepareOpFailsClosedWithoutScope(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, _ := createTestInstantReserveSwap(t, ctx, st.TransferStatusSenderKeyTweakPending)

	prepareOp := &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{
			Transfer: &pbspark.StartTransferRequest{TransferId: transfer.ID.String()},
		},
	}
	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Rollback(ctx, prepareOp)
	require.ErrorContains(t, err, "coordinator identity unavailable")

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updatedSwap.Status)
}

// TestReserveInstantFlowHandler_Commit_KeepsCreated pins the defining property
// of the reserve phase: commit applies the transfer commit but leaves the swap
// CREATED — CREATED-with-transfer is the reserved state the claim phase
// consumes. The transfer is already SENDER_KEY_TWEAKED (applySendTransferCommit
// short-circuits as an idempotent retry) so we can assert the swap-side effect
// in isolation.
func TestReserveInstantFlowHandler_Commit_KeepsCreated(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, transfer, _ := createTestInstantReserveSwap(t, ctx, st.TransferStatusSenderKeyTweaked)

	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Commit(ctx, &pbinternal.ReserveInstantStaticDepositUtxoSwapCommitRequest{
		TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: transfer.ID.String()},
	}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updated.Status, "reserve commit must keep the swap CREATED for the claim phase")
}

// TestReserveInstantFlowHandler_Commit_RejectsMissingTransferCommit rejects a
// commit payload without the embedded transfer commit.
func TestReserveInstantFlowHandler_Commit_RejectsMissingTransferCommit(t *testing.T) {
	t.Parallel()
	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Commit(t.Context(), &pbinternal.ReserveInstantStaticDepositUtxoSwapCommitRequest{})
	require.ErrorContains(t, err, "transfer_commit is required")
}

// TestReserveInstantFlowHandler_Prepare_RejectsMissingFields covers the
// structural fast-fails that run before any DB access.
func TestReserveInstantFlowHandler_Prepare_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	utxo := &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST}

	cases := []struct {
		name        string
		req         *pbinternal.ReserveInstantStaticDepositUtxoSwapRequest
		expectedErr string
	}{
		{
			name:        "missing utxo",
			req:         &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{Transfer: &pbspark.StartTransferRequest{TransferPackage: &pbspark.TransferPackage{}}},
			expectedErr: "on_chain_utxo is required",
		},
		{
			name:        "missing transfer",
			req:         &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{OnChainUtxo: utxo},
			expectedErr: "transfer is required",
		},
		{
			name:        "missing transfer package",
			req:         &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{OnChainUtxo: utxo, Transfer: &pbspark.StartTransferRequest{}},
			expectedErr: "transfer_package is required",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handler.Prepare(t.Context(), &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{OriginalRequest: tt.req})
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}

// TestReserveInstantFlowHandler_Prepare_RejectsInconsistentAmounts pins the
// instant amount invariants ported from the legacy participant.
func TestReserveInstantFlowHandler_Prepare_RejectsInconsistentAmounts(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewReserveInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	utxo := &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST}
	base := func() *pbinternal.ReserveInstantStaticDepositUtxoSwapRequest {
		return &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{
			OnChainUtxo: utxo,
			Transfer: &pbspark.StartTransferRequest{
				TransferId:                uuid.NewString(),
				OwnerIdentityPublicKey:    keys.GeneratePrivateKey().Public().Serialize(),
				ReceiverIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
				TransferPackage:           &pbspark.TransferPackage{},
			},
			ValueSats:        100,
			CreditAmountSats: 60,
		}
	}

	t.Run("credit exceeds value", func(t *testing.T) {
		req := base()
		req.CreditAmountSats = 150
		_, err := handler.Prepare(ctx, &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{OriginalRequest: req})
		require.ErrorContains(t, err, "exceeds value_sats")
	})

	t.Run("credit + secondary overflow does not wrap past the cap", func(t *testing.T) {
		req := base()
		req.CreditAmountSats = 100
		// A naive credit+secondary sum overflows int64 to a negative value that would
		// slip past a `> value_sats` cap; the headroom check must still reject it.
		req.SecondaryCreditAmountSats = math.MaxInt64
		req.RequestedSecondaryTransferId = uuid.NewString()
		_, err := handler.Prepare(ctx, &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{OriginalRequest: req})
		require.ErrorContains(t, err, "exceeds value_sats")
	})

	t.Run("secondary amount without secondary transfer id", func(t *testing.T) {
		req := base()
		req.SecondaryCreditAmountSats = 20
		_, err := handler.Prepare(ctx, &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{OriginalRequest: req})
		require.ErrorContains(t, err, "without requested_secondary_transfer_id")
	})

	t.Run("secondary transfer id without secondary amount", func(t *testing.T) {
		req := base()
		req.RequestedSecondaryTransferId = uuid.NewString()
		_, err := handler.Prepare(ctx, &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{OriginalRequest: req})
		require.ErrorContains(t, err, "without secondary_credit_amount_sats")
	})
}
