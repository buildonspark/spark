//go:build lightspark

package handler

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	sparkProto "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reloadSwapTransferForTest reads a transfer through the ctx transaction when
// one is active (handler mutations run inside it and are invisible to the raw
// client until commit).
func reloadSwapTransferForTest(t *testing.T, ctx context.Context, client *ent.Client, transferID uuid.UUID) *ent.Transfer {
	t.Helper()
	if dbFromCtx, err := ent.GetDbFromContext(ctx); err == nil {
		client = dbFromCtx
	}
	transfer, err := client.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	return transfer
}

func TestSwapCounterFlowHandler_ValidateDecisionAgainstPrepare(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapCounterTransferFlowHandler(cfg)
	transferID := uuid.NewString()
	adaptorPubKey := keys.GeneratePrivateKey().Public()
	prepare := &pbinternal.InitiateCounterTransferPrepareRequest{
		OriginalRequest: &pbinternal.InitiateCounterTransferRequest{
			Transfer:          &sparkProto.StartTransferRequest{TransferId: transferID},
			AdaptorPublicKeys: &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.Serialize()},
		},
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateCounterTransferCommitRequest{
		TransferId:       transferID,
		AdaptorPublicKey: adaptorPubKey.Serialize(),
	}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: transferID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, prepare))

	// A non-canonical but equal UUID (the decision canonicalizes it) must match
	// the verbatim prepared id — a raw string compare would spuriously reject.
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(
		&pbinternal.InitiateCounterTransferPrepareRequest{OriginalRequest: &pbinternal.InitiateCounterTransferRequest{
			Transfer:          &sparkProto.StartTransferRequest{TransferId: strings.ToUpper(transferID)},
			AdaptorPublicKeys: &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.Serialize()},
		}},
		&pbinternal.InitiateCounterTransferCommitRequest{TransferId: transferID, AdaptorPublicKey: adaptorPubKey.Serialize()}))

	err := handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateCounterTransferCommitRequest{
		TransferId:       uuid.NewString(),
		AdaptorPublicKey: adaptorPubKey.Serialize(),
	})
	require.ErrorContains(t, err, "does not match the prepared transfer id")

	err = handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateCounterTransferCommitRequest{
		TransferId:       transferID,
		AdaptorPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
	})
	require.ErrorContains(t, err, "adaptor public key does not match")

	// Encoding mismatch: the prepare op stores the client's verbatim adaptor key
	// (a non-canonical uncompressed encoding parses fine) while the commit carries
	// the canonical compressed form. The bind must compare the point, not raw
	// bytes, or a legitimate non-canonical adaptor key permanently fences the
	// commit on every non-coordinator SO.
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(
		&pbinternal.InitiateCounterTransferPrepareRequest{OriginalRequest: &pbinternal.InitiateCounterTransferRequest{
			Transfer:          &sparkProto.StartTransferRequest{TransferId: transferID},
			AdaptorPublicKeys: &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.ToBTCEC().SerializeUncompressed()},
		}},
		&pbinternal.InitiateCounterTransferCommitRequest{TransferId: transferID, AdaptorPublicKey: adaptorPubKey.Serialize()}))

	err = handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: uuid.NewString()})
	require.ErrorContains(t, err, "does not match the prepared transfer id")

	err = handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{})
	require.ErrorContains(t, err, "unexpected decision op type")
}

// A commit or rollback whose payload names a transfer of another type must be
// rejected before any mutation (defense in depth alongside the payload fence).
func TestSwapCounterFlowHandler_CommitAndRollback_RejectWrongTransferType(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{56})
	handler := NewSwapCounterTransferFlowHandler(cfg)

	// The PRIMARY leg's transfer is the wrong type for the counter handler.
	primary, _ := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)

	err := handler.Commit(ctx, &pbinternal.InitiateCounterTransferCommitRequest{
		TransferId:       primary.ID.String(),
		AdaptorPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
	})
	require.ErrorContains(t, err, "expected COUNTER_SWAP_V3")

	err = handler.Rollback(ctx, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: primary.ID.String()})
	require.ErrorContains(t, err, "expected COUNTER_SWAP_V3")

	reloadedPrimary := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, reloadedPrimary.Status)
}

func TestSwapCounterFlowHandler_Prepare_RejectsUnexpectedOpType(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapCounterTransferFlowHandler(cfg)

	_, err := handler.Prepare(t.Context(), &pbinternal.SendTransferPrepareRequest{})
	require.ErrorContains(t, err, "unexpected operation type")
}

func TestSwapCounterFlowHandler_Commit_RejectsUnexpectedOpType(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapCounterTransferFlowHandler(cfg)

	err := handler.Commit(t.Context(), &pbinternal.SendTransferCommitRequest{})
	require.ErrorContains(t, err, "unexpected operation type")
}

// A committed counter transfer can never legitimately be RETURNED before its
// commit applied — surface the invariant violation instead of skipping.
func TestSwapCounterFlowHandler_Commit_ReturnedCounter_Errors(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{50})
	handler := NewSwapCounterTransferFlowHandler(cfg)

	_, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := counter.Update().SetStatus(st.TransferStatusReturned).Save(ctx)
	require.NoError(t, err)

	adaptorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	err = handler.Commit(ctx, &pbinternal.InitiateCounterTransferCommitRequest{
		TransferId:       counter.ID.String(),
		AdaptorPublicKey: adaptorPubKey.Serialize(),
	})
	require.ErrorContains(t, err, "unexpected status")
}

// The counter Prepare must reject settlement while this SO's copy of the
// primary still has unsigned refund txs (its commit gossip hasn't landed) and
// accept once the signatures are applied.
func TestRequirePrimaryRefundSignaturesApplied(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{57})

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)

	err := requirePrimaryRefundSignaturesApplied(ctx, counter)
	require.ErrorContains(t, err, "refund signatures are not yet applied")

	primaryLeaves, err := primary.QueryTransferLeaves().All(ctx)
	require.NoError(t, err)
	for _, leaf := range primaryLeaves {
		signed, err := common.UpdateTxWithSignature(leaf.IntermediateRefundTx, 0, []byte{0x01})
		require.NoError(t, err)
		_, err = leaf.Update().SetIntermediateRefundTx(signed).Save(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, requirePrimaryRefundSignaturesApplied(ctx, counter))
}

func TestSwapCounterFlowHandler_Rollback_RejectsUnexpectedOpType(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapCounterTransferFlowHandler(cfg)

	err := handler.Rollback(t.Context(), &pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.ErrorContains(t, err, "unexpected operation type")
}

func TestSwapCounterFlowHandler_Rollback_NotFound_NoOp(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapCounterTransferFlowHandler(cfg)

	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: uuid.NewString()}))
}

// Rollback of a fenced counter must cancel the counter AND restore the
// primary from APPLYING_SENDER_KEY_TWEAK to its pre-flip pending status.
func TestSwapCounterFlowHandler_Rollback_RevertsFenceAndCancels(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{51})
	handler := NewSwapCounterTransferFlowHandler(cfg)

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := primary.Update().SetStatus(st.TransferStatusApplyingSenderKeyTweak).Save(ctx)
	require.NoError(t, err)

	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: counter.ID.String()}))

	reloadedPrimary := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, reloadedPrimary.Status)
	reloadedCounter := reloadSwapTransferForTest(t, ctx, dbCtx.Client, counter.ID)
	assert.Equal(t, st.TransferStatusReturned, reloadedCounter.Status)
}

// On the SO that coordinated the primary through the LEGACY path (marked by
// its PendingSendTransfer row), the fence revert must restore
// SENDER_INITIATED_COORDINATOR, not SENDER_KEY_TWEAK_PENDING.
func TestSwapCounterFlowHandler_Rollback_RestoresLegacyCoordinatorStatus(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{52})
	handler := NewSwapCounterTransferFlowHandler(cfg)

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	// A primary coordinated through the legacy path is still mid-swap at counter
	// rollback time, so its PendingSendTransfer row is STARTED — it is marked
	// FINISHED only by the receiver-side finalize (post-settlement) or a
	// cancel/rollback, neither of which has run here.
	_, err := dbCtx.Client.PendingSendTransfer.Create().
		SetTransferID(primary.ID).
		SetStatus(st.PendingSendTransferStatusPending).
		Save(ctx)
	require.NoError(t, err)
	_, err = primary.Update().SetStatus(st.TransferStatusApplyingSenderKeyTweak).Save(ctx)
	require.NoError(t, err)

	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: counter.ID.String()}))

	reloadedPrimary := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusSenderInitiatedCoordinator, reloadedPrimary.Status)
}

// A FINISHED PendingSendTransfer row must NOT be read as legacy coordination:
// a concurrent legacy attempt that reused this transfer id, lost the Transfer
// unique-key race, and marked its row FINISHED can leave a stale row behind a
// consensus-created primary. The fence revert must ignore it and restore
// SENDER_KEY_TWEAK_PENDING, not SENDER_INITIATED_COORDINATOR.
func TestSwapCounterFlowHandler_Rollback_IgnoresStaleFinishedPendingSendTransfer(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{55})
	handler := NewSwapCounterTransferFlowHandler(cfg)

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := dbCtx.Client.PendingSendTransfer.Create().
		SetTransferID(primary.ID).
		SetStatus(st.PendingSendTransferStatusFinished).
		Save(ctx)
	require.NoError(t, err)
	_, err = primary.Update().SetStatus(st.TransferStatusApplyingSenderKeyTweak).Save(ctx)
	require.NoError(t, err)

	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: counter.ID.String()}))

	reloadedPrimary := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, reloadedPrimary.Status)
}

// A redelivered rollback against an already-RETURNED counter must not touch
// the primary: the fence revert and the cancel run in one tx, so RETURNED
// implies the fence was already reverted — and the primary may since have
// been fenced again by a NEWER counter transfer.
func TestSwapCounterFlowHandler_Rollback_ReturnedCounter_DoesNotTouchFence(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{53})
	handler := NewSwapCounterTransferFlowHandler(cfg)

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := counter.Update().SetStatus(st.TransferStatusReturned).Save(ctx)
	require.NoError(t, err)
	// Simulate a newer counter having re-fenced the primary.
	_, err = primary.Update().SetStatus(st.TransferStatusApplyingSenderKeyTweak).Save(ctx)
	require.NoError(t, err)

	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: counter.ID.String()}))

	reloadedPrimary := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusApplyingSenderKeyTweak, reloadedPrimary.Status)
}

// A rollback delivered after the swap settled (both legs SENDER_KEY_TWEAKED)
// must be a no-op on both legs.
func TestSwapCounterFlowHandler_Rollback_AlreadyTweaked_NoOp(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{54})
	handler := NewSwapCounterTransferFlowHandler(cfg)

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := primary.Update().SetStatus(st.TransferStatusSenderKeyTweaked).Save(ctx)
	require.NoError(t, err)
	_, err = counter.Update().SetStatus(st.TransferStatusSenderKeyTweaked).Save(ctx)
	require.NoError(t, err)

	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateCounterTransferRollbackRequest{TransferId: counter.ID.String()}))

	reloadedPrimary := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusSenderKeyTweaked, reloadedPrimary.Status)
	reloadedCounter := reloadSwapTransferForTest(t, ctx, dbCtx.Client, counter.ID)
	assert.Equal(t, st.TransferStatusSenderKeyTweaked, reloadedCounter.Status)
}

// The prepare-op shape (reconciler presumed-abort path) is accepted and runs
// the same revert-and-cancel.
func TestSwapCounterFlowHandler_Rollback_AcceptsPrepareOpShape(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{55})
	handler := NewSwapCounterTransferFlowHandler(cfg)

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := primary.Update().SetStatus(st.TransferStatusApplyingSenderKeyTweak).Save(ctx)
	require.NoError(t, err)

	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateCounterTransferPrepareRequest{
		OriginalRequest: &pbinternal.InitiateCounterTransferRequest{
			Transfer:          &sparkProto.StartTransferRequest{TransferId: counter.ID.String()},
			PrimaryTransferId: primary.ID.String(),
		},
	}))

	reloadedPrimary := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, reloadedPrimary.Status)
	reloadedCounter := reloadSwapTransferForTest(t, ctx, dbCtx.Client, counter.ID)
	assert.Equal(t, st.TransferStatusReturned, reloadedCounter.Status)
}
