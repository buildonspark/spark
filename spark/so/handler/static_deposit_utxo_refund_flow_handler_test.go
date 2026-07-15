package handler

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStaticDepositRefundJobID_Deterministic pins that the signing job id is a
// pure function of (txid, vout) — every SO and the coordinator must derive the
// same id to correlate round-2 shares, and distinct utxos must not collide.
func TestStaticDepositRefundJobID_Deterministic(t *testing.T) {
	t.Parallel()
	txid := []byte{0x01, 0x02, 0x03}
	first := staticDepositRefundJobID(txid, 0)
	second := staticDepositRefundJobID(txid, 0)
	assert.Equal(t, first, second, "same (txid, vout) must derive the same job id")
	assert.NotEqual(t, first, staticDepositRefundJobID(txid, 1), "different vout must derive a different job id")
	assert.NotEqual(t, first, staticDepositRefundJobID([]byte{0x09}, 0), "different txid must derive a different job id")
}

// createTestRefundUtxoSwap builds the minimal entity graph the refund Commit /
// Rollback exercise: a static deposit address, a confirmed Utxo, and a
// REFUND-type UtxoSwap in the given status wired to that utxo.
func createTestRefundUtxoSwap(t *testing.T, ctx context.Context, status st.UtxoSwapStatus) (*ent.UtxoSwap, *ent.Utxo) {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{2})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	coordinatorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	depositAddress := createTestStaticDepositAddress(t, ctx, client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, client, depositAddress, 100)

	utxoSwap, err := client.UtxoSwap.Create().
		SetStatus(status).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(utxo.Amount).
		SetSspSignature([]byte("test_spend_tx_sighash")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey).
		SetConsensusManaged(true).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, addUtxoSwapToDepositAddress(ctx, client, depositAddress.ID, utxoSwap))
	return utxoSwap, utxo
}

func utxoProtoFor(u *ent.Utxo) *pbspark.UTXO {
	return &pbspark.UTXO{Txid: u.Txid, Vout: u.Vout, Network: pbspark.Network_REGTEST}
}

// TestStaticDepositUtxoRefundFlowHandler_Rollback_Created_Cancels covers the
// in-flight (pre-commit) case: a CREATED refund swap is cancelled. REFUND swaps
// have no transfer edge, so CancelUtxoSwap's SP-3261 guard is skipped.
func TestStaticDepositUtxoRefundFlowHandler_Rollback_Created_Cancels(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo := createTestRefundUtxoSwap(t, ctx, st.UtxoSwapStatusCreated)

	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoRefundRollbackRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updated.Status)
}

// TestStaticDepositUtxoRefundFlowHandler_Rollback_Completed_NoOp is the
// safety-critical case: a stray/redelivered rollback after Commit completed the
// swap must NOT cancel it and must return nil (not a plain error that loops the
// reconciler).
func TestStaticDepositUtxoRefundFlowHandler_Rollback_Completed_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo := createTestRefundUtxoSwap(t, ctx, st.UtxoSwapStatusCompleted)

	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoRefundRollbackRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updated.Status, "completed swap must not be cancelled")
}

// TestStaticDepositUtxoRefundFlowHandler_Rollback_Cancelled_NoOp pins the other
// idempotency leg: a rollback redelivered after the swap is already CANCELLED is a
// no-op (GetRegisteredUtxoSwapForUtxo excludes CANCELLED, so loadSwapForUtxo returns
// nil and Rollback returns nil), not an error that loops the reconciler.
func TestStaticDepositUtxoRefundFlowHandler_Rollback_Cancelled_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo := createTestRefundUtxoSwap(t, ctx, st.UtxoSwapStatusCancelled)

	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoRefundRollbackRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updated.Status, "already-cancelled swap stays cancelled")
}

// TestStaticDepositUtxoRefundFlowHandler_Rollback_AcceptsPrepareOp covers the
// reconciler echo-back path (prepare op rather than the canonical rollback op).
func TestStaticDepositUtxoRefundFlowHandler_Rollback_AcceptsPrepareOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo := createTestRefundUtxoSwap(t, ctx, st.UtxoSwapStatusCreated)

	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	prepareOp := &pbinternal.StaticDepositUtxoRefundPrepareRequest{
		OriginalRequest: &pbspark.InitiateStaticDepositUtxoRefundRequest{OnChainUtxo: utxoProtoFor(utxo)},
	}
	require.NoError(t, handler.Rollback(ctx, prepareOp))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updated.Status)
}

// TestStaticDepositUtxoRefundFlowHandler_Rollback_NoSwap_NoOp covers the case
// where Prepare never created a swap on this SO (utxo absent).
func TestStaticDepositUtxoRefundFlowHandler_Rollback_NoSwap_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	utxo := &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST}
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositUtxoRefundRollbackRequest{OnChainUtxo: utxo}))
}

// TestStaticDepositUtxoRefundFlowHandler_Rollback_RejectsMissingUtxo rejects a
// rollback payload with no utxo rather than panicking.
func TestStaticDepositUtxoRefundFlowHandler_Rollback_RejectsMissingUtxo(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	require.NotPanics(t, func() {
		err := handler.Rollback(t.Context(), &pbinternal.StaticDepositUtxoRefundRollbackRequest{})
		require.ErrorContains(t, err, "on_chain_utxo is required")
	})
}

// TestStaticDepositUtxoRefundFlowHandler_Rollback_RejectsUnexpectedOpType rejects
// an op of the wrong proto type.
func TestStaticDepositUtxoRefundFlowHandler_Rollback_RejectsUnexpectedOpType(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	require.NotPanics(t, func() {
		err := handler.Rollback(t.Context(), &pbinternal.StaticDepositUtxoRefundCommitRequest{})
		require.ErrorContains(t, err, "unexpected operation type")
	})
}

// TestStaticDepositUtxoRefundFlowHandler_Commit_Created_Completes covers the
// happy commit: a CREATED swap is marked COMPLETED.
func TestStaticDepositUtxoRefundFlowHandler_Commit_Created_Completes(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo := createTestRefundUtxoSwap(t, ctx, st.UtxoSwapStatusCreated)

	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	require.NoError(t, handler.Commit(ctx, &pbinternal.StaticDepositUtxoRefundCommitRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updated.Status)
}

// TestStaticDepositUtxoRefundFlowHandler_Commit_Idempotent covers gossip
// redelivery: a second Commit against an already-COMPLETED swap is a no-op.
func TestStaticDepositUtxoRefundFlowHandler_Commit_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	swap, utxo := createTestRefundUtxoSwap(t, ctx, st.UtxoSwapStatusCompleted)

	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	require.NoError(t, handler.Commit(ctx, &pbinternal.StaticDepositUtxoRefundCommitRequest{OnChainUtxo: utxoProtoFor(utxo)}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updated, err := dbTx.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updated.Status)
}

// TestStaticDepositUtxoRefundFlowHandler_Commit_NoSwap_ReturnsError pins the
// deliberate asymmetry with Rollback: a Commit for a utxo with no active swap is
// an invariant violation (the engine only commits after every SO prepared
// successfully), so it returns an error to keep the flow retrying rather than
// silently no-op'ing like Rollback does.
func TestStaticDepositUtxoRefundFlowHandler_Commit_NoSwap_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	utxo := &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST}
	err := handler.Commit(ctx, &pbinternal.StaticDepositUtxoRefundCommitRequest{OnChainUtxo: utxo})
	require.Error(t, err, "Commit with no active swap must return an error, not no-op")
}

// TestStaticDepositUtxoRefundFlowHandler_Prepare_RejectsMissingNonceCommitment
// upholds the 2PC invariant "Prepare success => commit-side success (modulo infra)":
// BuildCommitPayload parses the user nonce commitment to assemble the SigningResult,
// so Prepare must reject a missing/malformed commitment up front rather than letting
// it roll back an otherwise-prepared flow at commit time. The check runs before any
// DB access, so no entity graph is needed.
func TestStaticDepositUtxoRefundFlowHandler_Prepare_RejectsMissingNonceCommitment(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoRefundFlowHandler(nil)
	prepareOp := &pbinternal.StaticDepositUtxoRefundPrepareRequest{
		OriginalRequest: &pbspark.InitiateStaticDepositUtxoRefundRequest{
			OnChainUtxo:        &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST},
			RefundTxSigningJob: &pbspark.SigningJob{RawTx: []byte{0x01}}, // no SigningNonceCommitment
		},
	}
	_, err := handler.Prepare(t.Context(), prepareOp)
	require.ErrorContains(t, err, "signing_nonce_commitment is required")
}
