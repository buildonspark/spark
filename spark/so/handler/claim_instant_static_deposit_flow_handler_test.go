package handler

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the consensus.FlowHandler contract of the instant claim
// flow — the boundary the 2PC engine dispatches to. Fixtures mirror the state a
// consensus reserve leaves behind (INSTANT swap CREATED, sent primary transfer)
// plus the pieces claim's own Prepare adds; full multi-SO behavior is covered
// by the grpc_test_internal integration tests.

// claimableInstantSwapFixture is the reserved state the claim phase consumes,
// plus the pieces Prepare-boundary tests need to build a structurally valid
// claim request against it.
type claimableInstantSwapFixture struct {
	swap                *ent.UtxoSwap
	primary             *ent.Transfer
	secondary           *ent.Transfer
	utxo                *ent.Utxo
	ownerIdentityPubKey keys.Public
}

// createTestClaimableInstantSwap builds the reserved state the claim phase
// consumes: an INSTANT UtxoSwap in CREATED with a linked utxo edge and a SENT
// primary transfer, optionally pinned to a secondary transfer in the given
// status.
func createTestClaimableInstantSwap(t *testing.T, ctx context.Context, secondaryStatus *st.TransferStatus) *claimableInstantSwapFixture {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{11})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	coordinatorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	depositAddress := createTestStaticDepositAddress(t, ctx, client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	createTestBlockHeight(t, ctx, client, 100)
	utxo := createTestUtxo(t, ctx, client, depositAddress, 50)
	primary := createTestUtxoSwapTransfer(t, ctx, sspIdentityPubKey, ownerIdentityPubKey, client, st.TransferStatusSenderKeyTweaked, 90_000)

	create := client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetCreditAmountSats(90_000).
		SetSspSignature([]byte("test_ssp_quote_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserSignature([]byte("test_user_signature")).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey).
		SetRequestedTransferID(primary.ID).
		SetTransfer(primary).
		SetConsensusManaged(true)

	var secondary *ent.Transfer
	if secondaryStatus != nil {
		secondary = createTestUtxoSwapTransfer(t, ctx, sspIdentityPubKey, ownerIdentityPubKey, client, *secondaryStatus, 5_000)
		create = create.
			SetSecondaryCreditAmountSats(5_000).
			SetRequestedSecondaryTransferID(secondary.ID).
			SetSecondaryTransfer(secondary)
	}
	swap, err := create.Save(ctx)
	require.NoError(t, err)
	require.NoError(t, addUtxoSwapToDepositAddress(ctx, client, depositAddress.ID, swap))
	return &claimableInstantSwapFixture{
		swap:                swap,
		primary:             primary,
		secondary:           secondary,
		utxo:                utxo,
		ownerIdentityPubKey: ownerIdentityPubKey,
	}
}

// claimPrepareOpFor builds a prepare op that passes the structural guards at
// the top of Prepare: it names the fixture's confirmed utxo, carries the given
// spend tx, and a parseable nonce commitment.
func claimPrepareOpFor(t *testing.T, f *claimableInstantSwapFixture, transfer *pbspark.StartTransferRequest, spendTxBytes []byte) *pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest {
	t.Helper()
	return &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{
			OnChainUtxo: &pbspark.UTXO{
				Txid:    f.utxo.Txid,
				Vout:    f.utxo.Vout,
				Network: pbspark.Network_REGTEST,
			},
			SpendTxSigningJob: &pbspark.SigningJob{
				RawTx:                  spendTxBytes,
				SigningNonceCommitment: frost.GenerateSigningNonce().SigningCommitment().MarshalProto(),
			},
			TransferId: f.primary.ID.String(),
			Transfer:   transfer,
		},
	}
}

func claimRollbackOpFor(primary *ent.Transfer) *pbinternal.ClaimInstantStaticDepositUtxoSwapRollbackRequest {
	return &pbinternal.ClaimInstantStaticDepositUtxoSwapRollbackRequest{TransferId: primary.ID.String()}
}

// TestClaimInstantFlowHandler_Rollback_MutatesNothing pins the claim rollback's
// defining property (legacy parity — the legacy claim's rollback callback is
// empty): the reservation stays CREATED, the sent primary is untouched, and the
// prepared-but-unsent secondary is NOT returned — returning it would strand the
// reservation, since retries must reuse the pinned secondary transfer id and a
// RETURNED row blocks re-creating it.
func TestClaimInstantFlowHandler_Rollback_MutatesNothing(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	pending := st.TransferStatusSenderKeyTweakPending
	f := createTestClaimableInstantSwap(t, ctx, &pending)

	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(ctx, claimRollbackOpFor(f.primary)))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, f.swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updatedSwap.Status, "reservation must stay CREATED after claim rollback")
	updatedPrimary, err := dbTx.Transfer.Get(ctx, f.primary.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweaked, updatedPrimary.Status, "sent primary must be untouched")
	updatedSecondary, err := dbTx.Transfer.Get(ctx, f.secondary.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, updatedSecondary.Status, "prepared secondary must NOT be returned (transfer expiry owns it)")
}

// TestClaimInstantFlowHandler_Rollback_AcceptsBothOpShapes: both the canonical
// rollback payload and the reconciler-echoed prepare op are no-ops, including
// for a transfer with no reserved swap on this SO.
func TestClaimInstantFlowHandler_Rollback_AcceptsBothOpShapes(t *testing.T) {
	t.Parallel()
	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Rollback(t.Context(), &pbinternal.ClaimInstantStaticDepositUtxoSwapRollbackRequest{
		TransferId: uuid.NewString(),
	}))
	require.NoError(t, handler.Rollback(t.Context(), &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{TransferId: uuid.NewString()},
	}))
}

// TestClaimInstantFlowHandler_Rollback_RejectsUnexpectedOpType rejects an op of
// the wrong proto type so an engine dispatch bug surfaces instead of being
// absorbed by the no-op.
func TestClaimInstantFlowHandler_Rollback_RejectsUnexpectedOpType(t *testing.T) {
	t.Parallel()
	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Rollback(t.Context(), &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{})
	require.ErrorContains(t, err, "unexpected operation type")
}

// TestClaimInstantFlowHandler_Commit_CompletesReservation pins the commit
// effect: the CREATED reservation (sent primary, no secondary) is COMPLETED.
func TestClaimInstantFlowHandler_Commit_CompletesReservation(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	f := createTestClaimableInstantSwap(t, ctx, nil)

	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Commit(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
		TransferId: f.primary.ID.String(),
	}))

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, f.swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updatedSwap.Status)
}

// TestClaimInstantFlowHandler_Commit_AlreadyCompleted_NoOp is the redelivery
// idempotency leg.
func TestClaimInstantFlowHandler_Commit_AlreadyCompleted_NoOp(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	f := createTestClaimableInstantSwap(t, ctx, nil)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, CompleteUtxoSwap(ctx, f.swap))

	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	require.NoError(t, handler.Commit(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
		TransferId: f.primary.ID.String(),
	}))

	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, f.swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updatedSwap.Status)
}

// TestClaimInstantFlowHandler_Commit_UnsentSecondary_Errors: a commit whose
// payload lacks the secondary transfer commit while the reservation pins an
// unsent secondary must fail (CompleteUtxoSwap's sent-transfers requirement),
// so redelivery keeps retrying rather than completing a half-applied claim.
func TestClaimInstantFlowHandler_Commit_UnsentSecondary_Errors(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	pending := st.TransferStatusSenderKeyTweakPending
	f := createTestClaimableInstantSwap(t, ctx, &pending)

	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Commit(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
		TransferId: f.primary.ID.String(),
	})
	require.Error(t, err)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedSwap, err := dbTx.UtxoSwap.Get(ctx, f.swap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, updatedSwap.Status, "half-applied claim must not complete")
}

// TestClaimInstantFlowHandler_Commit_NoSwap_ReturnsError: commit for an unknown
// reservation errors so gossip redelivery keeps retrying.
func TestClaimInstantFlowHandler_Commit_NoSwap_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	err := handler.Commit(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
		TransferId: uuid.NewString(),
	})
	require.Error(t, err)
}

// TestClaimInstantFlowHandler_Prepare_RejectsMissingFields covers the
// structural validation at the top of Prepare.
func TestClaimInstantFlowHandler_Prepare_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))

	t.Run("wrong op type", func(t *testing.T) {
		_, err := handler.Prepare(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{})
		require.ErrorContains(t, err, "unexpected operation type")
	})
	t.Run("missing original request", func(t *testing.T) {
		_, err := handler.Prepare(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{})
		require.ErrorContains(t, err, "request is required")
	})
	t.Run("missing on_chain_utxo", func(t *testing.T) {
		_, err := handler.Prepare(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
			OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{},
		})
		require.ErrorContains(t, err, "on_chain_utxo is required")
	})
	utxo := &pbspark.UTXO{Txid: make([]byte, 32), Vout: 0, Network: pbspark.Network_REGTEST}
	t.Run("missing spend_tx_signing_job", func(t *testing.T) {
		_, err := handler.Prepare(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
			OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{OnChainUtxo: utxo},
		})
		require.ErrorContains(t, err, "spend_tx_signing_job is required")
	})
	t.Run("missing spend tx nonce commitment", func(t *testing.T) {
		_, err := handler.Prepare(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
			OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{
				OnChainUtxo:       utxo,
				SpendTxSigningJob: &pbspark.SigningJob{RawTx: []byte{0x01}},
			},
		})
		require.ErrorContains(t, err, "signing_nonce_commitment is required")
	})
	t.Run("invalid transfer_id", func(t *testing.T) {
		_, err := handler.Prepare(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
			OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{
				OnChainUtxo:       utxo,
				SpendTxSigningJob: validSpendTxSigningJobForTest(t),
				TransferId:        "not-a-uuid",
			},
		})
		require.ErrorContains(t, err, "invalid transfer_id")
	})
}

// validSpendTxSigningJobForTest builds a signing job whose nonce commitment
// parses, so structural tests can get past the commitment check.
func validSpendTxSigningJobForTest(t *testing.T) *pbspark.SigningJob {
	t.Helper()
	nonce := frost.GenerateSigningNonce()
	return &pbspark.SigningJob{
		RawTx:                  []byte{0x01},
		SigningNonceCommitment: nonce.SigningCommitment().MarshalProto(),
	}
}

// TestClaimInstantFlowHandler_Prepare_LinksUtxoToReservation drives the
// participant Prepare happy path for a reservation with no secondary credit:
// every linkUtxoToReservedSwap validation passes, the utxo edge is linked, and
// no round-2 share is produced since this SO is not in the coordinator's
// signing set.
func TestClaimInstantFlowHandler_Prepare_LinksUtxoToReservation(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	f := createTestClaimableInstantSwap(t, ctx, nil)
	// The fixture pre-links the utxo edge (the state Prepare leaves behind);
	// clear it so this test observes Prepare linking it.
	_, err := f.swap.Update().ClearUtxo().Save(ctx)
	require.NoError(t, err)

	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	spendTxBytes := createSpendTxBytesSpendingUtxo(t, f.utxo, f.ownerIdentityPubKey)
	result, err := handler.Prepare(ctx, claimPrepareOpFor(t, f, nil, spendTxBytes))
	require.NoError(t, err)
	assert.Nil(t, result, "no round-2 share expected when this SO is not in the signing set")

	linkedUtxo, err := f.swap.QueryUtxo().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, f.utxo.ID, linkedUtxo.ID)
}

// TestClaimInstantFlowHandler_Prepare_RejectsValidationMismatches covers the
// validation branches behind Prepare's structural guards: the confirmed-utxo
// lookup, the reservation amount match, the spend-tx-spends-target check, and
// the secondary-transfer cross-field checks against this SO's own reservation
// row.
func TestClaimInstantFlowHandler_Prepare_RejectsValidationMismatches(t *testing.T) {
	t.Parallel()
	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	pending := st.TransferStatusSenderKeyTweakPending

	t.Run("unknown utxo", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, nil)
		op := claimPrepareOpFor(t, f, nil, createSpendTxBytesSpendingUtxo(t, f.utxo, f.ownerIdentityPubKey))
		op.OriginalRequest.OnChainUtxo.Txid = make([]byte, 32)
		_, err := handler.Prepare(ctx, op)
		require.ErrorContains(t, err, "not found or not confirmed")
	})

	t.Run("utxo amount does not match reservation", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, nil)
		_, err := f.swap.Update().SetUtxoValueSats(f.utxo.Amount + 1).Save(ctx)
		require.NoError(t, err)
		_, err = handler.Prepare(ctx, claimPrepareOpFor(t, f, nil, createSpendTxBytesSpendingUtxo(t, f.utxo, f.ownerIdentityPubKey)))
		require.ErrorContains(t, err, "does not match swap utxo_value_sats")
	})

	t.Run("spend tx spends a different outpoint", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, nil)
		wrongSpendTx := createSpendTxBytesSpendingOutpoint(t, chainhash.Hash{0x01}, 0, f.ownerIdentityPubKey, int64(f.utxo.Amount))
		_, err := handler.Prepare(ctx, claimPrepareOpFor(t, f, nil, wrongSpendTx))
		require.ErrorContains(t, err, "unexpected spend transaction structure")
	})

	t.Run("transfer provided without secondary credit", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, nil)
		transfer := &pbspark.StartTransferRequest{
			TransferId:                uuid.NewString(),
			ReceiverIdentityPublicKey: f.ownerIdentityPubKey.Serialize(),
		}
		_, err := handler.Prepare(ctx, claimPrepareOpFor(t, f, transfer, createSpendTxBytesSpendingUtxo(t, f.utxo, f.ownerIdentityPubKey)))
		require.ErrorContains(t, err, "transfer must not be provided when there is no secondary credit amount")
	})

	t.Run("transfer missing with secondary credit", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, &pending)
		_, err := handler.Prepare(ctx, claimPrepareOpFor(t, f, nil, createSpendTxBytesSpendingUtxo(t, f.utxo, f.ownerIdentityPubKey)))
		require.ErrorContains(t, err, "transfer is required when secondary credit amount is set")
	})

	t.Run("secondary receiver does not match reservation user", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, &pending)
		transfer := &pbspark.StartTransferRequest{
			TransferId:                f.secondary.ID.String(),
			ReceiverIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
		}
		_, err := handler.Prepare(ctx, claimPrepareOpFor(t, f, transfer, createSpendTxBytesSpendingUtxo(t, f.utxo, f.ownerIdentityPubKey)))
		require.ErrorContains(t, err, "does not match utxo swap user identity public key")
	})

	t.Run("secondary transfer id does not match pinned id", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, &pending)
		transfer := &pbspark.StartTransferRequest{
			TransferId:                uuid.NewString(),
			ReceiverIdentityPublicKey: f.ownerIdentityPubKey.Serialize(),
		}
		_, err := handler.Prepare(ctx, claimPrepareOpFor(t, f, transfer, createSpendTxBytesSpendingUtxo(t, f.utxo, f.ownerIdentityPubKey)))
		require.ErrorContains(t, err, "transfer id does not match requested secondary transfer id")
	})

	t.Run("transfer package missing with secondary credit", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, &pending)
		transfer := &pbspark.StartTransferRequest{
			TransferId:                f.secondary.ID.String(),
			ReceiverIdentityPublicKey: f.ownerIdentityPubKey.Serialize(),
		}
		_, err := handler.Prepare(ctx, claimPrepareOpFor(t, f, transfer, createSpendTxBytesSpendingUtxo(t, f.utxo, f.ownerIdentityPubKey)))
		require.ErrorContains(t, err, "transfer package is required when secondary credit amount is set")
	})
}

// TestClaimInstantFlowHandler_Commit_MismatchedTransferCommit_Errors: a commit
// payload whose transfer commit names anything other than this SO's own pinned
// secondary transfer id must be rejected before any signatures or key tweaks
// are applied — fenced commit gossip cannot be redirected at an unrelated
// pre-commit transfer.
func TestClaimInstantFlowHandler_Commit_MismatchedTransferCommit_Errors(t *testing.T) {
	t.Parallel()
	handler := NewClaimInstantStaticDepositFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))

	t.Run("reservation with pinned secondary", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		pending := st.TransferStatusSenderKeyTweakPending
		f := createTestClaimableInstantSwap(t, ctx, &pending)
		err := handler.Commit(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
			TransferId:     f.primary.ID.String(),
			TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: uuid.NewString()},
		})
		require.ErrorContains(t, err, "does not match the reservation's pinned secondary transfer id")
	})

	t.Run("reservation without secondary", func(t *testing.T) {
		t.Parallel()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createTestClaimableInstantSwap(t, ctx, nil)
		err := handler.Commit(ctx, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
			TransferId:     f.primary.ID.String(),
			TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: uuid.NewString()},
		})
		require.ErrorContains(t, err, "does not match the reservation's pinned secondary transfer id")
	})
}
