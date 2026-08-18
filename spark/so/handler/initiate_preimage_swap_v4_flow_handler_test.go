package handler

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func v4PrepareOp(transferID string, reason pb.InitiatePreimageSwapRequest_Reason) *pbinternal.InitiatePreimageSwapV4PrepareRequest {
	return &pbinternal.InitiatePreimageSwapV4PrepareRequest{
		OriginalRequest: &pb.InitiatePreimageSwapV4Request{
			Reason:            reason,
			TransferV3Request: &pb.StartTransferV3Request{TransferId: transferID},
		},
	}
}

// The v4 fence must bind the same way the v3 one does, against the v4 carrier. It shares its
// implementation with v3, so this also pins that the delegation actually reaches it: a v4 handler
// that ignored the shared fence would accept every mismatch below.
func TestInitiatePreimageSwapV4ValidateDecisionAgainstPrepare(t *testing.T) {
	handler := NewInitiatePreimageSwapV4FlowHandler(sparktesting.TestConfig(t))
	transferID := uuid.New().String()
	// Non-canonical spelling: the prepared id is stored verbatim while decisions canonicalize.
	prepare := v4PrepareOp(strings.ToUpper(transferID), pb.InitiatePreimageSwapRequest_REASON_RECEIVE)

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiatePreimageSwapCommitRequest{TransferId: transferID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: transferID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, prepare))

	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare,
		&pbinternal.InitiatePreimageSwapCommitRequest{TransferId: uuid.NewString()}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare,
		&pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: uuid.NewString()}), "does not match the prepared transfer id")

	// The presumed-abort echo is the one version-typed decision payload, so v4 handles it itself
	// rather than delegating; a mismatched echo must still be refused.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare,
		v4PrepareOp(uuid.NewString(), pb.InitiatePreimageSwapRequest_REASON_RECEIVE)), "does not match the prepared transfer id")

	// A v3 carrier must not satisfy the v4 fence: the two prepare ops are distinct types and the
	// engine builds the handler from the op type alone.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(
		&pbinternal.InitiatePreimageSwapPrepareRequest{OriginalRequest: &pb.InitiatePreimageSwapRequest{}},
		&pbinternal.InitiatePreimageSwapCommitRequest{}), "unexpected prepare op type")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{}), "unexpected decision op type")
}

// A SEND-shaped v4 prepare inherits v3's requirement that its commit carry aggregated refund
// signatures. Reachable through isSendPackagePreimageSwapV4, which classifies on Reason alone
// because transfer_v3_request is required on v4.
func TestInitiatePreimageSwapV4CommitFenceRequiresLeafSignaturesForSend(t *testing.T) {
	handler := NewInitiatePreimageSwapV4FlowHandler(sparktesting.TestConfig(t))
	transferID := uuid.New().String()
	sendPrepare := v4PrepareOp(transferID, pb.InitiatePreimageSwapRequest_REASON_SEND)

	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(sendPrepare,
		&pbinternal.InitiatePreimageSwapCommitRequest{TransferId: transferID}), "carries no leaf signatures")
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(sendPrepare,
		&pbinternal.InitiatePreimageSwapCommitRequest{
			TransferId:     transferID,
			LeafSignatures: []*pbinternal.SendTransferLeafSignatures{{LeafId: uuid.NewString()}},
		}))

	// A RECEIVE prepare must NOT carry that requirement — a HODL receive legitimately commits
	// empty and settles later via ProvidePreimage.
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(
		v4PrepareOp(transferID, pb.InitiatePreimageSwapRequest_REASON_RECEIVE),
		&pbinternal.InitiatePreimageSwapCommitRequest{TransferId: transferID}))
}

// Prepare's own gates, before any database work: an unrecognized Reason would produce no shares
// and then be permanently fenced at commit, and REASON_SEND has no v4 implementation to fall into.
func TestInitiatePreimageSwapV4PrepareGates(t *testing.T) {
	handler := NewInitiatePreimageSwapV4FlowHandler(sparktesting.TestConfig(t))
	ctx := t.Context()

	_, err := handler.Prepare(ctx, &pbinternal.SendTransferPrepareRequest{})
	require.ErrorContains(t, err, "unexpected operation type")

	_, err = handler.Prepare(ctx, &pbinternal.InitiatePreimageSwapV4PrepareRequest{})
	require.ErrorContains(t, err, "request is required")

	_, err = handler.Prepare(ctx, v4PrepareOp(uuid.NewString(), pb.InitiatePreimageSwapRequest_Reason(99)))
	require.ErrorContains(t, err, "unrecognized preimage swap reason")
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// SEND is refused rather than validated as a receive: its refunds are HTLCs the P2TR
	// validator rejects, so falling through would fail for a misleading reason.
	_, err = handler.Prepare(ctx, v4PrepareOp(uuid.NewString(), pb.InitiatePreimageSwapRequest_REASON_SEND))
	require.ErrorContains(t, err, "REASON_RECEIVE only")
	require.Equal(t, codes.Unimplemented, status.Code(err))
}

// REASON_SEND is the proto3 default, so an unset Reason is the shape that must be refused.
func TestInitiatePreimageSwapV4EntrypointRefusesNonReceiveReason(t *testing.T) {
	handler := NewLightningHandler(sparktesting.TestConfig(t))
	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobInitiatePreimageSwapV4Enabled: 1,
	}))

	resp, err := handler.InitiatePreimageSwapV4(ctx, &pb.InitiatePreimageSwapV4Request{})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "REASON_RECEIVE only")
	require.Equal(t, codes.Unimplemented, status.Code(err))
}

// Signature.Serialize normalizes S into the low half, so a malleated form must be hand-encoded.
func malleateToHighS(t *testing.T, signature []byte) []byte {
	t.Helper()
	parsed, err := ecdsa.ParseDERSignature(signature)
	require.NoError(t, err)

	r, s := parsed.R(), parsed.S()
	s.Negate()
	rBytes, sBytes := r.Bytes(), s.Bytes()
	body := append(derInteger(rBytes[:]), derInteger(sBytes[:])...)
	malleated := append([]byte{0x30, byte(len(body))}, body...)

	require.NotEqual(t, signature, malleated)
	return malleated
}

func derInteger(value []byte) []byte {
	trimmed := value
	for len(trimmed) > 1 && trimmed[0] == 0x00 {
		trimmed = trimmed[1:]
	}
	if trimmed[0]&0x80 != 0 {
		trimmed = append([]byte{0x00}, trimmed...)
	}
	return append([]byte{0x02, byte(len(trimmed))}, trimmed...)
}

// Every case here is refused before any database work, so none of them need a fixture.
func TestInitiatePreimageSwapV4PrepareVerifiesCounterpartyManifestSignature(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{65})
	senderKey := keys.MustGeneratePrivateKeyFromRand(rng)
	counterpartyKey := keys.MustGeneratePrivateKeyFromRand(rng)
	impostorKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New().String()
	const leafID = "leaf-a"
	const edgeSats = 1000

	manifest := func(sats uint64) *pb.TransferManifest {
		return &pb.TransferManifest{
			Version:    1,
			TransferId: transferID,
			Network:    pb.Network_REGTEST,
			Edges: []*pb.ManifestEdge{{
				SenderIdentityPublicKey:   senderKey.Public().Serialize(),
				ReceiverIdentityPublicKey: counterpartyKey.Public().Serialize(),
				Amount:                    &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: sats}},
			}},
		}
	}
	sign := func(signer keys.Private, signed *pb.TransferManifest) []byte {
		hash, err := common.HashTransferManifest(signed)
		require.NoError(t, err)
		return ecdsa.Sign(signer.ToBTCEC(), hash).Serialize()
	}
	prepareOp := func(carried *pb.TransferManifest, signature []byte) *pbinternal.InitiatePreimageSwapV4PrepareRequest {
		return &pbinternal.InitiatePreimageSwapV4PrepareRequest{
			OriginalRequest: &pb.InitiatePreimageSwapV4Request{
				Reason:                        pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
				PaymentHash:                   make([]byte, 32),
				CounterpartyIdentityPublicKey: counterpartyKey.Public().Serialize(),
				CounterpartyManifestSignature: signature,
				TransferV3Request: &pb.StartTransferV3Request{
					TransferId: transferID,
					SenderPackages: []*pb.SenderTransferPackage{{
						OwnerIdentityPublicKey: senderKey.Public().Serialize(),
						TransferPackage: &pb.TransferPackage{
							KeyTweakPackage: map[string][]byte{"0000000000000000000000000000000000000000000000000000000000000001": {0x01}},
							UserSignature:   []byte{0x01},
						},
						ReceiverIdentityPublicKeys: map[string][]byte{leafID: counterpartyKey.Public().Serialize()},
					}},
					TransferManifest: carried,
				},
			},
		}
	}

	handler := NewInitiatePreimageSwapV4FlowHandler(sparktesting.TestConfig(t))

	tests := []struct {
		name          string
		op            *pbinternal.InitiatePreimageSwapV4PrepareRequest
		expectedError string
	}{
		{
			"a valid signature passes the check and fails later for want of a database",
			prepareOp(manifest(edgeSats), sign(counterpartyKey, manifest(edgeSats))),
			"",
		},
		{
			"a missing signature on a receive is refused",
			prepareOp(manifest(edgeSats), nil),
			"counterparty_manifest_signature is required",
		},
		{
			"a signature by another key is refused",
			prepareOp(manifest(edgeSats), sign(impostorKey, manifest(edgeSats))),
			"manifest signature is invalid",
		},
		{
			"a signature over another manifest is refused",
			prepareOp(manifest(edgeSats), sign(counterpartyKey, manifest(edgeSats+1))),
			"manifest signature is invalid",
		},
		{
			"a malleated high-S signature is refused",
			prepareOp(manifest(edgeSats), malleateToHighS(t, sign(counterpartyKey, manifest(edgeSats)))),
			"manifest signature is invalid",
		},
		{
			"a signature with no manifest is refused",
			prepareOp(nil, sign(counterpartyKey, manifest(edgeSats))),
			"counterparty_manifest_signature set without a transfer_manifest",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handler.Prepare(t.Context(), tc.op)

			require.Error(t, err)
			if tc.expectedError == "" {
				assert.NotContains(t, err.Error(), "counterparty_manifest_signature")
				assert.NotContains(t, err.Error(), "manifest signature is invalid")
				assert.ErrorContains(t, err, "failed to get current tx for request")
				return
			}
			require.ErrorContains(t, err, tc.expectedError)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func assertPreimageSwapRolledBack(t *testing.T, ctx context.Context, transferID uuid.UUID, leafID uuid.UUID) {
	t.Helper()
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rolledBackTransfer, err := dbTx.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, rolledBackTransfer.Status, "transfer should be RETURNED")

	unlockedLeaf, err := dbTx.TreeNode.Get(ctx, leafID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAvailable, unlockedLeaf.Status, "leaf should be unlocked")
}

// The prepare-op echo case is the regression guard: it is version-typed, so an unoverridden v4
// handler errors on every reconciler tick and the leaves stay locked forever.
func TestInitiatePreimageSwapV4FlowHandler_Rollback_CancelsPendingTransfer_BothOpShapes(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewInitiatePreimageSwapV4FlowHandler(nil)

	canonicalTransfer, canonicalLeaf := createTestPreimageSwapTransfer(t, ctx, st.TransferStatusSenderKeyTweakPending)
	require.NoError(t, handler.Rollback(ctx, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: canonicalTransfer.ID.String()}))
	assertPreimageSwapRolledBack(t, ctx, canonicalTransfer.ID, canonicalLeaf.ID)

	echoedTransfer, echoedLeaf := createTestPreimageSwapTransfer(t, ctx, st.TransferStatusSenderKeyTweakPending)
	require.NoError(t, handler.Rollback(ctx, v4PrepareOp(echoedTransfer.ID.String(), pb.InitiatePreimageSwapRequest_REASON_RECEIVE)))
	assertPreimageSwapRolledBack(t, ctx, echoedTransfer.ID, echoedLeaf.ID)

	// Gossip redelivery of the echo, and a transfer this SO never prepared, are both no-ops.
	require.NoError(t, handler.Rollback(ctx, v4PrepareOp(echoedTransfer.ID.String(), pb.InitiatePreimageSwapRequest_REASON_RECEIVE)))
	require.NoError(t, handler.Rollback(ctx, v4PrepareOp(uuid.NewString(), pb.InitiatePreimageSwapRequest_REASON_RECEIVE)))
}

func TestInitiatePreimageSwapV4FlowHandler_Rollback_RejectsUnusableOps(t *testing.T) {
	t.Parallel()
	handler := NewInitiatePreimageSwapV4FlowHandler(nil)

	// The engine resolves the handler from the op type, so accepting a v3 echo here would cancel
	// a transfer resolved from an unvalidated carrier.
	require.ErrorContains(t, handler.Rollback(t.Context(), &pbinternal.InitiatePreimageSwapPrepareRequest{}), "unexpected operation type")
	require.ErrorContains(t, handler.Rollback(t.Context(), &pbinternal.InitiatePreimageSwapCommitRequest{}), "unexpected operation type")
	require.ErrorContains(t, handler.Rollback(t.Context(), &pbinternal.InitiatePreimageSwapV4PrepareRequest{}), "transfer_id is required")
	require.ErrorContains(t, handler.Rollback(t.Context(), &pbinternal.InitiatePreimageSwapRollbackRequest{}), "transfer_id is required")
}
