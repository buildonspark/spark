package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common/keys"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCheckRefundTimelockMonotonicity exercises the stale-replay guard for
// FinalizeRenewRefundTimelock. Within a renew epoch the leaf's RawTx
// timelock must strictly decrease per finalize (NextSequence enforces
// this on the producer); the guard rejects payloads whose timelock is
// higher than or equal to the current leaf's.
func TestCheckRefundTimelockMonotonicity(t *testing.T) {
	leafID := uuid.New()

	// Use spark sequence-flag bits to match production tx construction.
	const seqFlag = 1 << 30 // BIP68 type flag, same shape as spark.InitialSequence().

	tests := []struct {
		name             string
		currentTimelock  uint32
		incomingTimelock uint32
		wantCode         codes.Code // OK == nil
	}{
		{
			name:             "incoming strictly lower — legitimate refund renewal",
			currentTimelock:  spark.InitialTimeLock,
			incomingTimelock: spark.InitialTimeLock - spark.TimeLockInterval,
			wantCode:         codes.OK,
		},
		{
			name:             "incoming equal to current — stale (byte-equality short-circuit handles true redelivery elsewhere)",
			currentTimelock:  spark.InitialTimeLock - spark.TimeLockInterval,
			incomingTimelock: spark.InitialTimeLock - spark.TimeLockInterval,
			wantCode:         codes.AlreadyExists,
		},
		{
			name:             "incoming strictly higher — stale replay from before a newer refund landed",
			currentTimelock:  spark.InitialTimeLock - 2*spark.TimeLockInterval,
			incomingTimelock: spark.InitialTimeLock - spark.TimeLockInterval,
			wantCode:         codes.AlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentTx := createValidTestTransactionBytesWithSequence(t, tt.currentTimelock|seqFlag)
			incomingTx := createValidTestTransactionBytesWithSequence(t, tt.incomingTimelock|seqFlag)

			err := checkRefundTimelockMonotonicity(currentTx, incomingTx, leafID)

			if tt.wantCode == codes.OK {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("expected gRPC code %v, got %v (err=%v)", tt.wantCode, got, err)
			}
		})
	}
}

func TestFinalizeRenewTimelockRejectsMalformedRequestsWithoutPanic(t *testing.T) {
	handler := NewInternalRenewLeafHandler(nil)

	tests := []struct {
		name    string
		call    func() error
		wantErr string
	}{
		{
			name: "node finalize nil request",
			call: func() error {
				return handler.FinalizeRenewNodeTimelock(t.Context(), nil)
			},
			wantErr: "request is required",
		},
		{
			name: "node finalize missing node",
			call: func() error {
				return handler.FinalizeRenewNodeTimelock(t.Context(), &pbinternal.FinalizeRenewNodeTimelockRequest{})
			},
			wantErr: "node is required",
		},
		{
			name: "node finalize missing split node",
			call: func() error {
				return handler.FinalizeRenewNodeTimelock(t.Context(), &pbinternal.FinalizeRenewNodeTimelockRequest{
					Node: &pbinternal.TreeNode{},
				})
			},
			wantErr: "split_node is required",
		},
		{
			name: "refund finalize nil request",
			call: func() error {
				return handler.FinalizeRenewRefundTimelock(t.Context(), nil)
			},
			wantErr: "request is required",
		},
		{
			name: "refund finalize missing node",
			call: func() error {
				return handler.FinalizeRenewRefundTimelock(t.Context(), &pbinternal.FinalizeRenewRefundTimelockRequest{})
			},
			wantErr: "node is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() {
				err = tt.call()
			})
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestFinalizeRenewNodeTimelockRejectsSplitNodeFromDifferentTree(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{11})

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	extendedLeafTree := createTestRenewTree(t, ctx, ownerIdentityPubKey)
	otherTree := createTestRenewTree(t, ctx, ownerIdentityPubKey)
	extendedLeaf := createTestRenewTreeNode(t, ctx, rng, dbClient, extendedLeafTree, keyshare, nil, 0)

	const sequenceFlag = 1 << 30
	currentRenewableTx := createValidTestTransactionBytesWithSequence(t, (spark.RenewTimelockThreshold-1)|sequenceFlag)
	extendedLeaf, err = extendedLeaf.Update().SetRawTx(currentRenewableTx).Save(ctx)
	require.NoError(t, err)

	incomingLeafTx := createValidTestTransactionBytesWithSequence(t, (spark.RenewTimelockThreshold-2)|sequenceFlag)
	splitNodeID := uuid.New()
	req := &pbinternal.FinalizeRenewNodeTimelockRequest{
		SplitNode: &pbinternal.TreeNode{
			Id:                  splitNodeID.String(),
			Value:               extendedLeaf.Value,
			VerifyingPubkey:     extendedLeaf.VerifyingPubkey.Serialize(),
			OwnerIdentityPubkey: extendedLeaf.OwnerIdentityPubkey.Serialize(),
			OwnerSigningPubkey:  extendedLeaf.OwnerSigningPubkey.Serialize(),
			RawTx:               currentRenewableTx,
			DirectTx:            currentRenewableTx,
			TreeId:              otherTree.ID.String(),
			SigningKeyshareId:   keyshare.ID.String(),
			Vout:                0,
		},
		Node: &pbinternal.TreeNode{
			Id:                     extendedLeaf.ID.String(),
			RawTx:                  incomingLeafTx,
			RawRefundTx:            extendedLeaf.RawRefundTx,
			DirectTx:               extendedLeaf.DirectTx,
			DirectRefundTx:         extendedLeaf.DirectRefundTx,
			DirectFromCpfpRefundTx: extendedLeaf.DirectFromCpfpRefundTx,
			TreeId:                 extendedLeafTree.ID.String(),
			SigningKeyshareId:      keyshare.ID.String(),
			VerifyingPubkey:        extendedLeaf.VerifyingPubkey.Serialize(),
			OwnerIdentityPubkey:    extendedLeaf.OwnerIdentityPubkey.Serialize(),
			OwnerSigningPubkey:     extendedLeaf.OwnerSigningPubkey.Serialize(),
			Value:                  extendedLeaf.Value,
			Vout:                   0,
		},
	}

	handler := NewInternalRenewLeafHandler(nil)
	err = handler.FinalizeRenewNodeTimelock(ctx, req)
	require.ErrorContains(t, err, "does not match extended leaf tree")

	exists, err := dbClient.TreeNode.Query().Where(treenode.IDEQ(splitNodeID)).Exist(ctx)
	require.NoError(t, err)
	require.False(t, exists, "malformed renew finalize must not create a split node in another tree")

	updatedLeaf, err := dbClient.TreeNode.Get(ctx, extendedLeaf.ID)
	require.NoError(t, err)
	updatedTree, err := updatedLeaf.QueryTree().Only(ctx)
	require.NoError(t, err)
	require.Equal(t, extendedLeafTree.ID, updatedTree.ID)
	_, err = updatedLeaf.QueryParent().Only(ctx)
	require.True(t, ent.IsNotFound(err), "malformed renew finalize must not reparent the leaf")
}

// TestCheckNodeRenewPrecondition exercises the stale-replay guard for
// FinalizeRenewNodeTimelock. validateAndConstructNodeTimelock only
// produces a renew-node payload when the existing leaf's RawTx timelock
// is at or below the renew threshold (300). The guard rejects finalizes
// against leaves whose current timelock is above that — those are stale
// payloads from before a newer renew-node added a chain layer that reset
// the timelock high.
func TestCheckNodeRenewPrecondition(t *testing.T) {
	leafID := uuid.New()
	const seqFlag = 1 << 30

	tests := []struct {
		name            string
		currentTimelock uint32
		wantCode        codes.Code
	}{
		{
			name:            "current at zero — eligible for renew-node-zero",
			currentTimelock: 0,
			wantCode:        codes.OK,
		},
		{
			name:            "current well below threshold — eligible for renew-node",
			currentTimelock: 100,
			wantCode:        codes.OK,
		},
		{
			name:            "current exactly at threshold — eligible (matches validateAndConstructNodeTimelock's <=)",
			currentTimelock: spark.RenewTimelockThreshold,
			wantCode:        codes.OK,
		},
		{
			name:            "current just above threshold — stale payload (a newer renew-node already happened)",
			currentTimelock: spark.RenewTimelockThreshold + 1,
			wantCode:        codes.AlreadyExists,
		},
		{
			name:            "current near InitialTimeLock — clearly stale",
			currentTimelock: spark.InitialTimeLock - spark.TimeLockInterval,
			wantCode:        codes.AlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentTx := createValidTestTransactionBytesWithSequence(t, tt.currentTimelock|seqFlag)

			err := checkNodeRenewPrecondition(currentTx, leafID)

			if tt.wantCode == codes.OK {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("expected gRPC code %v, got %v (err=%v)", tt.wantCode, got, err)
			}
		})
	}
}
