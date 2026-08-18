//go:build lightspark

package handler

import (
	"context"
	"crypto/sha256"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Lightning sends must refuse leaves already at the renewal floor: unlike
// regular transfers (ValidateSequence), the HTLC path subtracts
// HTLCSequenceOffset rather than TimeLockInterval and historically had no
// floor, so a floor-timelock leaf could initiate a send whose HTLC refund the
// receiver can never claim (the claim-time floor rejects it unconditionally).
func TestInitiatePreimageSwapEnforcesHtlcTimelockFloor(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{99})
	ownerPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	paymentHash := sha256.Sum256([]byte("htlc-timelock-floor"))

	cfg := sparktesting.TestConfig(t)
	lightningHandler := NewLightningHandler(cfg)

	setupLeaf := func(t *testing.T, ctx context.Context, refundTimelock uint32) *ent.TreeNode {
		tx, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		keyshare := createTestSigningKeyshare(t, ctx, rng, tx)
		tree := createTestTreeForClaim(t, ctx, ownerPrivKey.Public(), tx)

		leafAmount := int64(1000)
		parentTxBytes, parentTxHash := createVersion3ParentTx(t, ownerPrivKey.Public(), leafAmount, 0)
		cpfpRefundTx := createVersion3CPFPRefundTx(t, parentTxHash, 0, ownerPrivKey.Public(), leafAmount, (1<<30)|refundTimelock)

		leaf, err := tx.TreeNode.Create().
			SetStatus(st.TreeNodeStatusAvailable).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(keyshare).
			SetValue(uint64(leafAmount)).
			SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetOwnerIdentityPubkey(ownerPrivKey.Public()).
			SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetRawTx(parentTxBytes).
			SetRawRefundTx(cpfpRefundTx).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)
		return leaf
	}

	newRequest := func(t *testing.T, leaf *ent.TreeNode) (*pb.InitiatePreimageSwapRequest, map[string]*pb.SecretProof) {
		transferID := uuid.New()
		keyTweakPackage, keyTweakProofs := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leaf.ID})
		pkg := &pb.TransferPackage{
			LeavesToSend:    []*pb.UserSignedTxSigningJob{{LeafId: leaf.ID.String(), RawTx: createTestTxBytes(t, 1000)}},
			KeyTweakPackage: keyTweakPackage,
		}
		signTransferPackage(t, pkg, transferID, ownerPrivKey)

		req := &pb.InitiatePreimageSwapRequest{
			PaymentHash:               paymentHash[:],
			InvoiceAmount:             &pb.InvoiceAmount{ValueSats: 900},
			Reason:                    pb.InitiatePreimageSwapRequest_REASON_SEND,
			ReceiverIdentityPublicKey: receiverPrivKey.Public().Serialize(),
			TransferRequest: &pb.StartTransferRequest{
				TransferId:                transferID.String(),
				OwnerIdentityPublicKey:    ownerPrivKey.Public().Serialize(),
				ReceiverIdentityPublicKey: receiverPrivKey.Public().Serialize(),
				ExpiryTime:                timestamppb.New(time.Now().Add(time.Hour)),
				TransferPackage:           pkg,
			},
		}
		return req, keyTweakProofs
	}

	knobCtx := func(ctx context.Context, floorOn float64) context.Context {
		return knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobEnforceLightningHtlcTimelockFloor: floorOn,
		}))
	}

	t.Run("floor on rejects a leaf at the renewal floor", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		leaf := setupLeaf(t, ctx, 100)
		req, proofs := newRequest(t, leaf)
		_, err := lightningHandler.GetPreimageShare(knobCtx(ctx, 1), req, nil, nil, nil, proofs)
		require.ErrorContains(t, err, "must be renewed before it can be lightning-sent")
		st, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.InvalidArgument, st.Code())
		var errorInfo *errdetails.ErrorInfo
		for _, d := range st.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok {
				errorInfo = info
			}
		}
		require.NotNil(t, errorInfo)
		require.Equal(t, sparkerrors.ReasonInvalidArgumentLeafRenewalRequired, errorInfo.GetReason())
		require.Equal(t, leaf.ID.String(), errorInfo.GetMetadata()[sparkerrors.ErrorMetadataLeafID])
	})

	t.Run("floor on rejects a non-aligned timelock that rounds to the floor", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		leaf := setupLeaf(t, ctx, 199)
		req, proofs := newRequest(t, leaf)
		_, err := lightningHandler.GetPreimageShare(knobCtx(ctx, 1), req, nil, nil, nil, proofs)
		require.ErrorContains(t, err, "must be renewed before it can be lightning-sent")
	})

	t.Run("floor on allows the minimum sendable timelock", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		leaf := setupLeaf(t, ctx, 200)
		req, proofs := newRequest(t, leaf)
		_, err := lightningHandler.GetPreimageShare(knobCtx(ctx, 1), req, nil, nil, nil, proofs)
		// Past the floor check and into reconstruct-and-compare, which rejects
		// the deliberately-mismatched client HTLC bytes.
		require.ErrorContains(t, err, "cpfp leaf refund tx mismatch")
	})

	t.Run("floor on allows a leaf above the renewal floor", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		leaf := setupLeaf(t, ctx, 2000)
		req, proofs := newRequest(t, leaf)
		_, err := lightningHandler.GetPreimageShare(knobCtx(ctx, 1), req, nil, nil, nil, proofs)
		require.ErrorContains(t, err, "cpfp leaf refund tx mismatch")
	})

	t.Run("floor off preserves legacy behavior for a floor-timelock leaf", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		leaf := setupLeaf(t, ctx, 100)
		req, proofs := newRequest(t, leaf)
		_, err := lightningHandler.GetPreimageShare(knobCtx(ctx, 0), req, nil, nil, nil, proofs)
		require.ErrorContains(t, err, "cpfp leaf refund tx mismatch")
	})
}
