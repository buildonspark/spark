//go:build lightspark

package handler

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	sparkProto "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseSwapTransferRequest_RejectsPreWitnessedRefund verifies swap v3
// ingest rejects a refund tx that already carries a witness — the invariant
// the counter leg's requirePrimaryRefundSignaturesApplied gate relies on to
// distinguish an SE-applied signature from a user-forged one.
func TestParseSwapTransferRequest_RejectsPreWitnessedRefund(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{60})
	adaptorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leafID := uuid.NewString()

	prevOut := wire.OutPoint{Hash: [32]byte{0x11}, Index: 0}
	tx := wire.NewMsgTx(wire.TxVersion)
	in := wire.NewTxIn(&prevOut, nil, nil)
	in.Witness = wire.TxWitness{[]byte{0x01}} // forged witness
	tx.AddTxIn(in)
	pkScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)
	tx.AddTxOut(wire.NewTxOut(900, pkScript))
	rawTx, err := common.SerializeTx(tx)
	require.NoError(t, err)

	transfer := &sparkProto.StartTransferRequest{
		TransferId:                uuid.NewString(),
		OwnerIdentityPublicKey:    keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
		ReceiverIdentityPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
		TransferPackage: withDummyPackageAuth(&sparkProto.TransferPackage{
			LeavesToSend: []*sparkProto.UserSignedTxSigningJob{createSendTransferUserSignedJob(t, rng, leafID, rawTx)},
		}),
	}
	_, err = parseSwapTransferRequest(transfer, &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.Serialize()})
	require.ErrorContains(t, err, "must be submitted unsigned")
}

func TestParseSwapTransferRequest_Validation(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{40})
	adaptorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	validAdaptorKeys := &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.Serialize()}
	validTransfer := func() *sparkProto.StartTransferRequest {
		return &sparkProto.StartTransferRequest{
			TransferId:                uuid.NewString(),
			OwnerIdentityPublicKey:    sender.Serialize(),
			ReceiverIdentityPublicKey: receiver.Serialize(),
			TransferPackage: &sparkProto.TransferPackage{
				LeavesToSend:    []*sparkProto.UserSignedTxSigningJob{},
				KeyTweakPackage: map[string][]byte{"op": {0x1}},
				UserSignature:   []byte{0x1},
			},
		}
	}

	tests := []struct {
		name        string
		transfer    *sparkProto.StartTransferRequest
		adaptorKeys *sparkProto.AdaptorPublicKeyPackage
		wantErr     string
	}{
		{
			name:        "nil transfer",
			transfer:    nil,
			adaptorKeys: validAdaptorKeys,
			wantErr:     "transfer is required",
		},
		{
			name: "missing transfer package",
			transfer: func() *sparkProto.StartTransferRequest {
				r := validTransfer()
				r.TransferPackage = nil
				return r
			}(),
			adaptorKeys: validAdaptorKeys,
			wantErr:     "transfer_package is required",
		},
		{
			name: "direct leaves rejected",
			transfer: func() *sparkProto.StartTransferRequest {
				r := validTransfer()
				r.TransferPackage.DirectLeavesToSend = []*sparkProto.UserSignedTxSigningJob{{}}
				return r
			}(),
			adaptorKeys: validAdaptorKeys,
			wantErr:     "direct transactions should not be provided",
		},
		{
			name: "direct-from-cpfp leaves rejected",
			transfer: func() *sparkProto.StartTransferRequest {
				r := validTransfer()
				r.TransferPackage.DirectFromCpfpLeavesToSend = []*sparkProto.UserSignedTxSigningJob{{}}
				return r
			}(),
			adaptorKeys: validAdaptorKeys,
			wantErr:     "direct transactions should not be provided",
		},
		{
			name:        "invalid adaptor public key",
			transfer:    validTransfer(),
			adaptorKeys: &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: []byte{0x1, 0x2}},
			wantErr:     "unable to parse adaptor public key",
		},
		{
			name:        "missing adaptor public key package",
			transfer:    validTransfer(),
			adaptorKeys: nil,
			wantErr:     "unable to parse adaptor public key",
		},
		{
			name: "invalid transfer id",
			transfer: func() *sparkProto.StartTransferRequest {
				r := validTransfer()
				r.TransferId = "not-a-uuid"
				return r
			}(),
			adaptorKeys: validAdaptorKeys,
			wantErr:     "invalid transfer id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSwapTransferRequest(tt.transfer, tt.adaptorKeys)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSwapPrimaryFlowHandler_ValidateDecisionAgainstPrepare(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapPrimaryTransferFlowHandler(cfg)
	transferID := uuid.NewString()
	adaptorPubKey := keys.GeneratePrivateKey().Public()
	prepare := &pbinternal.InitiateSwapPrimaryTransferPrepareRequest{
		OriginalRequest: &sparkProto.InitiateSwapPrimaryTransferRequest{
			Transfer:          &sparkProto.StartTransferRequest{TransferId: transferID},
			AdaptorPublicKeys: &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.Serialize()},
		},
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       transferID,
		AdaptorPublicKey: adaptorPubKey.Serialize(),
	}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateSwapPrimaryTransferRollbackRequest{TransferId: transferID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, prepare))

	// A non-canonical but equal UUID (the decision canonicalizes it) must match
	// the verbatim prepared id — a raw string compare would spuriously reject.
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(
		&pbinternal.InitiateSwapPrimaryTransferPrepareRequest{OriginalRequest: &sparkProto.InitiateSwapPrimaryTransferRequest{
			Transfer:          &sparkProto.StartTransferRequest{TransferId: strings.ToUpper(transferID)},
			AdaptorPublicKeys: &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.Serialize()},
		}},
		&pbinternal.InitiateSwapPrimaryTransferCommitRequest{TransferId: transferID, AdaptorPublicKey: adaptorPubKey.Serialize()}))

	err := handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       uuid.NewString(),
		AdaptorPublicKey: adaptorPubKey.Serialize(),
	})
	require.ErrorContains(t, err, "does not match the prepared transfer id")

	err = handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       transferID,
		AdaptorPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
	})
	require.ErrorContains(t, err, "adaptor public key does not match")

	// Encoding mismatch: the prepare op stores whatever bytes the client sent for
	// the adaptor key (a non-canonical 65-byte uncompressed encoding parses fine),
	// while the commit carries the canonical 33-byte Serialize() form. The bind
	// must compare the point, not raw bytes, or a legitimate non-canonical adaptor
	// key permanently fences the commit on every non-coordinator SO.
	uncompressedPrepare := &pbinternal.InitiateSwapPrimaryTransferPrepareRequest{
		OriginalRequest: &sparkProto.InitiateSwapPrimaryTransferRequest{
			Transfer:          &sparkProto.StartTransferRequest{TransferId: transferID},
			AdaptorPublicKeys: &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.ToBTCEC().SerializeUncompressed()},
		},
	}
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(uncompressedPrepare, &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       transferID,
		AdaptorPublicKey: adaptorPubKey.Serialize(), // compressed
	}))

	err = handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferRollbackRequest{})
	require.ErrorContains(t, err, "unexpected decision op type")
}

// A commit or rollback whose payload names a transfer of another type must be
// rejected before any mutation (defense in depth alongside the payload fence).
func TestSwapPrimaryFlowHandler_CommitAndRollback_RejectWrongTransferType(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{43})
	handler := NewSwapPrimaryTransferFlowHandler(cfg)

	// The COUNTER leg's transfer is the wrong type for the primary handler.
	_, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)

	err := handler.Commit(ctx, &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       counter.ID.String(),
		AdaptorPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
	})
	require.ErrorContains(t, err, "expected PRIMARY_SWAP_V3")

	err = handler.Rollback(ctx, &pbinternal.InitiateSwapPrimaryTransferRollbackRequest{TransferId: counter.ID.String()})
	require.ErrorContains(t, err, "expected PRIMARY_SWAP_V3")

	reloadedCounter := reloadSwapTransferForTest(t, ctx, dbCtx.Client, counter.ID)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, reloadedCounter.Status)
}

func TestSwapPrimaryFlowHandler_Prepare_RejectsUnexpectedOpType(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapPrimaryTransferFlowHandler(cfg)

	_, err := handler.Prepare(t.Context(), &pbinternal.SendTransferPrepareRequest{})
	require.ErrorContains(t, err, "unexpected operation type")
}

func TestSwapPrimaryFlowHandler_Commit_RejectsUnexpectedOpType(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapPrimaryTransferFlowHandler(cfg)

	err := handler.Commit(t.Context(), &pbinternal.SendTransferCommitRequest{})
	require.ErrorContains(t, err, "unexpected operation type")
}

// A primary transfer cancelled while the commit gossip was in flight (swap v3
// primaries stay cancellable until a counter transfer exists) must be left
// untouched by a late commit.
func TestSwapPrimaryFlowHandler_Commit_SkipsReturnedTransfer(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{41})
	handler := NewSwapPrimaryTransferFlowHandler(cfg)

	primary, _ := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := primary.Update().SetStatus(st.TransferStatusReturned).Save(ctx)
	require.NoError(t, err)

	adaptorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	err = handler.Commit(ctx, &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       primary.ID.String(),
		LeafSignatures:   []*pbinternal.SendTransferLeafSignatures{{LeafId: uuid.NewString(), RefundSignature: []byte{0x1}}},
		AdaptorPublicKey: adaptorPubKey.Serialize(),
	})
	require.NoError(t, err)

	reloaded := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusReturned, reloaded.Status)
}

// An EXPIRED primary is likewise cancelled and must be left untouched by a late
// commit.
func TestSwapPrimaryFlowHandler_Commit_SkipsExpiredTransfer(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{44})
	handler := NewSwapPrimaryTransferFlowHandler(cfg)

	primary, _ := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := primary.Update().SetStatus(st.TransferStatusExpired).Save(ctx)
	require.NoError(t, err)

	err = handler.Commit(ctx, &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       primary.ID.String(),
		LeafSignatures:   []*pbinternal.SendTransferLeafSignatures{{LeafId: uuid.NewString(), RefundSignature: []byte{0x1}}},
		AdaptorPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
	})
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusExpired, reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID).Status)
}

// Mixed mode: a legacy counter settles the primary via SettleSwapKeyTweak with
// no per-SO fence, so a lagging SO can find this leg already at a receiver-side
// status before the primary's consensus commit lands. The commit must STILL
// apply the refund signatures there (not silently skip), or the 2PC ledger would
// record the commit as applied while that SO never wrote the refund-tx witness.
// The transfer here carries fake signatures, so reaching the apply path
// surfaces as a signature-verification error — proving the gate routes to apply
// (a skip would return nil).
func TestSwapPrimaryFlowHandler_Commit_AppliesAfterReceiverAdvanced(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{45})
	handler := NewSwapPrimaryTransferFlowHandler(cfg)

	primary, _ := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := primary.Update().SetStatus(st.TransferStatusReceiverKeyTweakApplied).Save(ctx)
	require.NoError(t, err)

	err = handler.Commit(ctx, &pbinternal.InitiateSwapPrimaryTransferCommitRequest{
		TransferId:       primary.ID.String(),
		LeafSignatures:   []*pbinternal.SendTransferLeafSignatures{{LeafId: uuid.NewString(), RefundSignature: []byte{0x1}}},
		AdaptorPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
	})
	require.Error(t, err, "receiver-advanced primary must reach the apply path, not skip")
}

func TestSwapPrimaryFlowHandler_Rollback_RejectsUnexpectedOpType(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	handler := NewSwapPrimaryTransferFlowHandler(cfg)

	err := handler.Rollback(t.Context(), &pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.ErrorContains(t, err, "unexpected operation type")
}

func TestSwapPrimaryFlowHandler_Rollback_CancelsPendingTransfer_BothOpShapes(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{42})
	handler := NewSwapPrimaryTransferFlowHandler(cfg)

	primary, _ := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateSwapPrimaryTransferRollbackRequest{TransferId: primary.ID.String()}))
	reloaded := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary.ID)
	assert.Equal(t, st.TransferStatusReturned, reloaded.Status)

	// Redelivery is a no-op.
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateSwapPrimaryTransferRollbackRequest{TransferId: primary.ID.String()}))

	// The prepare-op shape (reconciler presumed-abort path) works too.
	primary2, _ := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateSwapPrimaryTransferPrepareRequest{
		OriginalRequest: &sparkProto.InitiateSwapPrimaryTransferRequest{
			Transfer: &sparkProto.StartTransferRequest{TransferId: primary2.ID.String()},
		},
	}))
	reloaded2 := reloadSwapTransferForTest(t, ctx, dbCtx.Client, primary2.ID)
	assert.Equal(t, st.TransferStatusReturned, reloaded2.Status)

	// A transfer that never reached this SO is a no-op.
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiateSwapPrimaryTransferRollbackRequest{TransferId: uuid.NewString()}))
}

// TestBuildSwapCoordinatorSigningJobs_RejectsMissingOrForeignLeaf verifies the
// coordinator-side leaf gate (shared by both swap legs) rejects a leaf that
// doesn't exist or is owned by someone other than the sender BEFORE any Prepare
// fan-out, via the owner-filtered preload's count-mismatch check.
func TestBuildSwapCoordinatorSigningJobs_RejectsMissingOrForeignLeaf(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{91})
	sender := keys.MustGeneratePrivateKeyFromRand(rng)
	adaptorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	unsignedRefundTx := func() []byte {
		tx := wire.NewMsgTx(wire.TxVersion)
		tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: [32]byte{0x22}, Index: 0}, nil, nil)) // no witness
		pkScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
		require.NoError(t, err)
		tx.AddTxOut(wire.NewTxOut(900, pkScript))
		rawTx, err := common.SerializeTx(tx)
		require.NoError(t, err)
		return rawTx
	}

	parsedFor := func(leafID string) parsedSwapTransferRequest {
		req := &sparkProto.StartTransferRequest{
			TransferId:                uuid.NewString(),
			OwnerIdentityPublicKey:    sender.Public().Serialize(),
			ReceiverIdentityPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			TransferPackage: withDummyPackageAuth(&sparkProto.TransferPackage{
				LeavesToSend: []*sparkProto.UserSignedTxSigningJob{createSendTransferUserSignedJob(t, rng, leafID, unsignedRefundTx())},
			}),
		}
		parsed, err := parseSwapTransferRequest(req, &sparkProto.AdaptorPublicKeyPackage{AdaptorPublicKey: adaptorPubKey.Serialize()})
		require.NoError(t, err)
		return parsed
	}

	t.Run("nonexistent leaf", func(t *testing.T) {
		_, err := buildSwapCoordinatorSigningJobs(ctx, parsedFor(uuid.NewString()))
		require.ErrorContains(t, err, "preload missed leaves")
	})

	t.Run("foreign-owned leaf", func(t *testing.T) {
		// createDbLeaf assigns a random owner, which is not the sender above, so
		// the owner-filtered query returns zero rows and the count check fires.
		leaf := createDbLeaf(t, ctx, false)
		require.NotEqual(t, sender.Public(), leaf.node.OwnerIdentityPubkey, "leaf must be foreign-owned for this case")
		_, err := buildSwapCoordinatorSigningJobs(ctx, parsedFor(leaf.node.ID.String()))
		require.ErrorContains(t, err, "preload missed leaves")
	})
}
