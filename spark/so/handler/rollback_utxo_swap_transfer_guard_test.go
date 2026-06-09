package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/distributed-lab/gripmock"
	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
)

func createSwapTransferWithStatus(t *testing.T, ctx context.Context, client *ent.Client, rng *rand.ChaCha8, status st.TransferStatus) *ent.Transfer {
	t.Helper()
	transfer, err := client.Transfer.Create().
		SetSenderIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetReceiverIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetStatus(status).
		SetTotalValue(2000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetType(st.TransferTypePrimarySwapV3).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)
	return transfer
}

// createOrphanedSwapForRollback builds the state of a static-deposit claim that
// died mid-commit: a CREATED, non-refund UtxoSwap whose transfer reached the
// given status, linked only by requested_transfer_id (the swap->transfer edge
// was never persisted). Returns the swap.
func createOrphanedSwapForRollback(t *testing.T, ctx context.Context, client *ent.Client, rng *rand.ChaCha8, cfg *so.Config, utxo *ent.Utxo, transfer *ent.Transfer) *ent.UtxoSwap {
	t.Helper()
	swap, err := client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetCreditAmountSats(transfer.TotalValue).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetUserIdentityPublicKey(transfer.ReceiverIdentityPubkey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(transfer.ID).
		Save(ctx)
	require.NoError(t, err)
	return swap
}

// Rolling back a swap whose transfer is already sent (receiver-claimable) must be
// refused — otherwise the transfer is orphaned and the customer can claim the
// leaves with no completed swap backing them. The swap->transfer edge is unset
// (only requested_transfer_id), modelling a claim that died before completion.
// SP-3261.
func TestRollbackUtxoSwap_RefusesWhenTransferSent(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil))

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{1})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	transfer := createSwapTransferWithStatus(t, ctx, sessionCtx.Client, rng, st.TransferStatusSenderKeyTweaked)
	swap := createOrphanedSwapForRollback(t, ctx, sessionCtx.Client, rng, cfg, utxo, transfer)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.ErrorContains(t, err, "already sent")

	reloaded, err := sessionCtx.Client.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, reloaded.Status, "swap must stay CREATED so it can be completed, not orphaned")
}

// The guard must not over-block: a non-refund swap whose transfer has not been
// sent is still safe to roll back (the legitimate first-phase-failure path).
func TestRollbackUtxoSwap_AllowsWhenNonRefundTransferNotSent(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil))

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{2})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	transfer := createSwapTransferWithStatus(t, ctx, sessionCtx.Client, rng, st.TransferStatusSenderKeyTweakPending)
	swap := createOrphanedSwapForRollback(t, ctx, sessionCtx.Client, rng, cfg, utxo, transfer)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.NoError(t, err)

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	reloaded, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, reloaded.Status)
}

// When the transfer genuinely does not exist yet (the common rollback during the
// swap-creation phase, before the transfer is created), there is nothing
// claimable to orphan, so the rollback must still succeed. A blanket fail-closed
// guard would refuse this and re-strand the swap. SP-3261.
func TestRollbackUtxoSwap_AllowsWhenTransferDoesNotExist(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil))

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{3})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Swap created, but the transfer was never created: requested_transfer_id
	// points at a row that does not exist and the edge is unset.
	swap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetCreditAmountSats(2000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetUserIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.NoError(t, err)

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	reloaded, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, reloaded.Status)
}

// Narrow lower-level test (CancelUtxoSwap directly): the fail-closed-on-transient-
// error path is invisible at the RollbackUtxoSwap boundary — a cancelled context
// would fail the handler's earlier queries, not the guard, so the guard's behavior
// can't be isolated from above. Per testing-philosophy this fund-safety internal
// warrants a direct test: an unreadable transfer state must block the cancel
// rather than fall through to it. SP-3261.
func TestCancelUtxoSwap_FailsClosedWhenTransferStateUnreadable(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{4})

	swap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxoValueSats(2000).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetCreditAmountSats(2000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetUserIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetCoordinatorIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRequestedTransferID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	// A cancelled context makes the transfer lookup fail with a non-NotFound error.
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	err = CancelUtxoSwap(canceledCtx, swap)
	require.ErrorContains(t, err, "cannot determine transfer state")

	reloaded, err := sessionCtx.Client.UtxoSwap.Get(ctx, swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, reloaded.Status, "must not cancel when transfer state is unreadable")
}
