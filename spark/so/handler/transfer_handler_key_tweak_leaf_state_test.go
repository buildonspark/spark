//go:build lightspark

package handler

import (
	"context"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSettleReceiverKeyTweakRejectsNonTransferLockedLeaf(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{71})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	nonTransferLockedStatuses := []st.TreeNodeStatus{
		st.TreeNodeStatusCreating,
		st.TreeNodeStatusAvailable,
		st.TreeNodeStatusFrozenByIssuer,
		st.TreeNodeStatusSplitLocked,
		st.TreeNodeStatusSplitted,
		st.TreeNodeStatusAggregated,
		st.TreeNodeStatusAggregateLock,
		st.TreeNodeStatusInvestigation,
		st.TreeNodeStatusLost,
		st.TreeNodeStatusReimbursed,
		st.TreeNodeStatusRenewLocked,
	}

	for _, leafStatus := range nonTransferLockedStatuses {
		t.Run(string(leafStatus), func(t *testing.T) {
			leaf, transfer, transferLeaf := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)
			leaf, err := leaf.Update().SetStatus(leafStatus).Save(ctx)
			require.NoError(t, err)

			originalOwnerIdentityPubkey := leaf.OwnerIdentityPubkey.Serialize()
			originalOwnerSigningPubkey := leaf.OwnerSigningPubkey.Serialize()
			originalKeyTweak := append([]byte(nil), transferLeaf.KeyTweak...)

			err = handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
				TransferId: transfer.ID.String(),
				Action:     pbinternal.SettleKeyTweakAction_COMMIT,
			})
			require.Error(t, err)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.ErrorContains(t, err, "must be TRANSFER_LOCKED or exited to L1 to claim receiver key tweak")

			updatedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
			require.NoError(t, err)
			require.Equal(t, leafStatus, updatedLeaf.Status)
			require.Equal(t, originalOwnerIdentityPubkey, updatedLeaf.OwnerIdentityPubkey.Serialize())
			require.Equal(t, originalOwnerSigningPubkey, updatedLeaf.OwnerSigningPubkey.Serialize())

			updatedTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
			require.NoError(t, err)
			require.Equal(t, originalKeyTweak, updatedTransferLeaf.KeyTweak)

			updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
			require.NoError(t, err)
			require.Equal(t, st.TransferStatusReceiverKeyTweakLocked, updatedTransfer.Status)
		})
	}
}

func TestSettleReceiverKeyTweakAppliesExitedToL1LeafPreservingStatus(t *testing.T) {
	exitedStatuses := []st.TreeNodeStatus{
		st.TreeNodeStatusOnChain,
		st.TreeNodeStatusExited,
		st.TreeNodeStatusParentExited,
	}

	for i, leafStatus := range exitedStatuses {
		t.Run(string(leafStatus), func(t *testing.T) {
			ctx, sessionCtx := db.ConnectToTestPostgres(t)
			rng := rand.NewChaCha8([32]byte{byte(73 + i)})
			cfg := sparktesting.TestConfig(t)
			handler := NewTransferHandler(cfg)

			leaf, transfer, transferLeaf := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)
			leaf, err := leaf.Update().SetStatus(leafStatus).Save(ctx)
			require.NoError(t, err)
			originalOwnerSigningPubkey := leaf.OwnerSigningPubkey.Serialize()

			err = handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
				TransferId: transfer.ID.String(),
				Action:     pbinternal.SettleKeyTweakAction_COMMIT,
			})
			require.NoError(t, err)

			entTx, err := ent.GetTxFromContext(ctx)
			require.NoError(t, err)
			require.NoError(t, entTx.Commit())

			// Ownership moves to the receiver, but the on-chain status is preserved.
			updatedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
			require.NoError(t, err)
			require.Equal(t, leafStatus, updatedLeaf.Status)
			require.Equal(t, transfer.ReceiverIdentityPubkey.Serialize(), updatedLeaf.OwnerIdentityPubkey.Serialize())
			require.NotEqual(t, originalOwnerSigningPubkey, updatedLeaf.OwnerSigningPubkey.Serialize())

			updatedTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
			require.NoError(t, err)
			require.Empty(t, updatedTransferLeaf.KeyTweak)

			updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
			require.NoError(t, err)
			require.Equal(t, st.TransferStatusReceiverKeyTweakApplied, updatedTransfer.Status)
		})
	}
}

func TestSettleReceiverKeyTweakAppliesTransferLockedLeaf(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{72})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	leaf, transfer, transferLeaf := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)
	originalOwnerSigningPubkey := leaf.OwnerSigningPubkey.Serialize()

	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.NoError(t, err)

	// SettleReceiverKeyTweak intentionally does not commit the surrounding
	// ent transaction (the caller owns the commit lifecycle), so commit here
	// before asserting through the plain client.
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	updatedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, updatedLeaf.Status)
	require.Equal(t, transfer.ReceiverIdentityPubkey.Serialize(), updatedLeaf.OwnerIdentityPubkey.Serialize())
	require.NotEqual(t, originalOwnerSigningPubkey, updatedLeaf.OwnerSigningPubkey.Serialize())

	updatedTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	require.Empty(t, updatedTransferLeaf.KeyTweak)

	updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusReceiverKeyTweakApplied, updatedTransfer.Status)
}

func createReceiverKeyTweakSettlementFixture(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	cfg *so.Config,
	rng *rand.ChaCha8,
) (*ent.TreeNode, *ent.Transfer, *ent.TransferLeaf) {
	t.Helper()

	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), client)
	leaf := createTestTreeNode(t, ctx, rng, client, tree, keyshare)
	transfer := createTestTransfer(t, ctx, rng, client, st.TransferStatusReceiverKeyTweakLocked)
	transferLeaf := createTestTransferLeaf(t, ctx, client, transfer, leaf)

	keyTweakBytes := createReceiverClaimKeyTweakBytes(t, cfg, rng, leaf.ID)
	transferLeaf, err := transferLeaf.Update().SetKeyTweak(keyTweakBytes).Save(ctx)
	require.NoError(t, err)

	return leaf, transfer, transferLeaf
}

func createReceiverClaimKeyTweakBytes(t *testing.T, cfg *so.Config, rng *rand.ChaCha8, leafID uuid.UUID) []byte {
	t.Helper()

	tweakPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretInt := new(big.Int).SetBytes(tweakPrivKey.Serialize())
	shares, err := secretsharing.SplitSecretWithProofs(
		secretInt,
		secp256k1.S256().N,
		int(cfg.Threshold),
		len(cfg.SigningOperatorMap),
	)
	require.NoError(t, err)
	require.NotEmpty(t, shares)

	secretShareBytes := make([]byte, 32)
	shares[0].Share.FillBytes(secretShareBytes)

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		LeafId: leafID.String(),
		SecretShareTweak: &pb.SecretShare{
			SecretShare: secretShareBytes,
			Proofs:      shares[0].Proofs,
		},
		PubkeySharesTweak: buildValidPubkeySharesTweak(t, cfg, shares[0].Proofs),
	}
	keyTweakBytes, err := proto.Marshal(claimKeyTweak)
	require.NoError(t, err)
	return keyTweakBytes
}

// createMultiLeafReceiverKeyTweakSettlementFixture builds one transfer at
// RECEIVER_KEY_TWEAK_LOCKED with numLeaves TRANSFER_LOCKED leaves, each with
// its own signing keyshare and a stored claim key tweak.
func createMultiLeafReceiverKeyTweakSettlementFixture(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	cfg *so.Config,
	rng *rand.ChaCha8,
	numLeaves int,
) (*ent.Transfer, []*ent.TreeNode, []*ent.TransferLeaf) {
	t.Helper()

	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), client)
	transfer := createTestTransfer(t, ctx, rng, client, st.TransferStatusReceiverKeyTweakLocked)

	leaves := make([]*ent.TreeNode, 0, numLeaves)
	transferLeaves := make([]*ent.TransferLeaf, 0, numLeaves)
	for range numLeaves {
		keyshare := createTestSigningKeyshare(t, ctx, rng, client)
		leaf := createTestTreeNode(t, ctx, rng, client, tree, keyshare)
		transferLeaf := createTestTransferLeaf(t, ctx, client, transfer, leaf)
		keyTweakBytes := createReceiverClaimKeyTweakBytes(t, cfg, rng, leaf.ID)
		transferLeaf, err := transferLeaf.Update().SetKeyTweak(keyTweakBytes).Save(ctx)
		require.NoError(t, err)
		leaves = append(leaves, leaf)
		transferLeaves = append(transferLeaves, transferLeaf)
	}
	return transfer, leaves, transferLeaves
}

func TestSettleReceiverKeyTweakAppliesMultiLeafBatch(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{81})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	transfer, leaves, transferLeaves := createMultiLeafReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng, 5)

	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.NoError(t, err)

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	for i, leaf := range leaves {
		updatedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		require.Equal(t, transfer.ReceiverIdentityPubkey.Serialize(), updatedLeaf.OwnerIdentityPubkey.Serialize())
		require.NotEqual(t, leaf.OwnerSigningPubkey.Serialize(), updatedLeaf.OwnerSigningPubkey.Serialize())

		updatedTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaves[i].ID)
		require.NoError(t, err)
		require.Empty(t, updatedTransferLeaf.KeyTweak)
	}

	updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusReceiverKeyTweakApplied, updatedTransfer.Status)
}

func TestSettleReceiverKeyTweakRejectsSharedKeyshareAcrossLeaves(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{82})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	transfer := createTestTransfer(t, ctx, rng, sessionCtx.Client, st.TransferStatusReceiverKeyTweakLocked)

	sharedKeyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	var leaves []*ent.TreeNode
	for range 2 {
		leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, sharedKeyshare)
		transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)
		keyTweakBytes := createReceiverClaimKeyTweakBytes(t, cfg, rng, leaf.ID)
		_, err := transferLeaf.Update().SetKeyTweak(keyTweakBytes).Save(ctx)
		require.NoError(t, err)
		leaves = append(leaves, leaf)
	}

	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.Error(t, err)
	require.Equal(t, codes.Internal, status.Code(err))
	require.ErrorContains(t, err, "is referenced by both leaf")

	// Neither leaf's ownership moved.
	for _, leaf := range leaves {
		unchangedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		require.Equal(t, leaf.OwnerIdentityPubkey.Serialize(), unchangedLeaf.OwnerIdentityPubkey.Serialize())
		require.Equal(t, leaf.OwnerSigningPubkey.Serialize(), unchangedLeaf.OwnerSigningPubkey.Serialize())
	}
}

func TestSettleReceiverKeyTweakSurfacesPerKeyshareErrorWhenHydrationFails(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{83})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	transfer, _, _ := createMultiLeafReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng, 2)

	// Strip one keyshare's main-DB secret and point it at an ephemeral version
	// that is unreachable in this test session (no ephemeral provider). The
	// batch hydration prefetch fails, but the claim must surface the precise
	// per-keyshare error from TweakKeyShare's own hydration attempt rather
	// than a batch-stage failure.
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf(func(tnq *ent.TreeNodeQuery) {
		tnq.WithSigningKeyshare()
	}).All(ctx)
	require.NoError(t, err)
	require.Len(t, transferLeaves, 2)
	starvedKeyshare := transferLeaves[0].Edges.Leaf.Edges.SigningKeyshare
	_, err = starvedKeyshare.Update().ClearSecretShare().SetSecretVersion(1).Save(ctx)
	require.NoError(t, err)

	err = handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, starvedKeyshare.ID.String())
	require.ErrorContains(t, err, "ephemeral DB is unavailable")
	// The batched rotation must keep per-leaf attribution: the error names the
	// leaf whose keyshare failed, not just the transfer.
	require.ErrorContains(t, err, transferLeaves[0].Edges.Leaf.ID.String())
}

// TestSettleReceiverKeyTweakHoldsKeyshareRowLocksUntilCommit proves the batch
// keyshare read actually row-locks: while the settle transaction is still
// open, a second DB session's FOR UPDATE NOWAIT on the same keyshare rows
// must fail with lock_not_available (SQLSTATE 55P03), and must succeed once
// the transaction commits. This is the property that shields the hydrated
// secrets and the per-keyshare rotation CAS from a concurrent rotation.
func TestSettleReceiverKeyTweakHoldsKeyshareRowLocksUntilCommit(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{84})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	transfer, leaves, _ := createMultiLeafReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng, 3)
	keyshareIDs := make([]uuid.UUID, 0, len(leaves))
	for _, leaf := range leaves {
		keyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		require.NoError(t, err)
		keyshareIDs = append(keyshareIDs, keyshare.ID)
	}

	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.NoError(t, err)

	// The settle transaction is still open: a separate session must not be
	// able to lock any of the keyshare rows.
	_, lockErr := sessionCtx.Client.SigningKeyshare.Query().
		Where(signingkeyshare.IDIn(keyshareIDs...)).
		ForUpdate(sql.WithLockAction(sql.NoWait)).
		All(t.Context())
	require.Error(t, lockErr)
	require.True(t, db.IsLockNotAvailableError(lockErr), "expected lock_not_available (55P03), got: %v", lockErr)

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	// After commit the same probe must succeed.
	unlocked, err := sessionCtx.Client.SigningKeyshare.Query().
		Where(signingkeyshare.IDIn(keyshareIDs...)).
		ForUpdate(sql.WithLockAction(sql.NoWait)).
		All(t.Context())
	require.NoError(t, err)
	require.Len(t, unlocked, len(keyshareIDs))
}

// TestInitiateSettleReceiverKeyTweakRejectsSharedKeyshareAcrossLeaves proves
// the Phase-1 copy of the duplicate-keyshare guard: a claim package whose
// leaves share a signing keyshare must be rejected by
// InitiateSettleReceiverKeyTweak itself — before any key tweak is durably
// stored — not just by the Phase-2 apply.
func TestInitiateSettleReceiverKeyTweakRejectsSharedKeyshareAcrossLeaves(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{85})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	// The receiver's identity key must be known so the claim package
	// signature verifies; createTestTransfer generates an opaque one.
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	transfer, err := sessionCtx.Client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweaked).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPubKey).
		SetReceiverIdentityPubkey(receiverPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	sharedKeyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	claimKeyTweaks := &pb.ClaimLeafKeyTweaks{}
	var transferLeafIDs []uuid.UUID
	for range 2 {
		leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, sharedKeyshare)
		transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)
		transferLeafIDs = append(transferLeafIDs, transferLeaf.ID)
		leafTweak := &pb.ClaimLeafKeyTweak{}
		require.NoError(t, proto.Unmarshal(createReceiverClaimKeyTweakBytes(t, cfg, rng, leaf.ID), leafTweak))
		claimKeyTweaks.LeavesToReceive = append(claimKeyTweaks.LeavesToReceive, leafTweak)
	}

	// Encrypt this SO's slice to its own identity key and sign the package
	// with the receiver identity key, mirroring the SDK's claim package.
	plaintext, err := proto.Marshal(claimKeyTweaks)
	require.NoError(t, err)
	encryptionKey, err := eciesgo.NewPublicKeyFromBytes(cfg.IdentityPrivateKey.Public().Serialize())
	require.NoError(t, err)
	encrypted, err := eciesgo.Encrypt(encryptionKey, plaintext)
	require.NoError(t, err)
	encryptedPackage := map[string][]byte{cfg.Identifier: encrypted}
	signature := ecdsa.Sign(receiverPrivKey.ToBTCEC(), common.GetClaimPackageSigningPayload(transfer.ID, encryptedPackage))

	err = handler.InitiateSettleReceiverKeyTweak(ctx, &pbinternal.InitiateSettleReceiverKeyTweakRequest{
		TransferId:                    transfer.ID.String(),
		EncryptedClaimKeyTweakPackage: encryptedPackage,
		ClaimSignature:                signature.Serialize(),
	})
	require.Error(t, err)
	require.Equal(t, codes.Internal, status.Code(err))
	require.ErrorContains(t, err, "is referenced by both leaf")

	// Phase 1 aborted before storing anything: no key tweak was persisted and
	// the transfer never advanced past SenderKeyTweaked.
	for _, transferLeafID := range transferLeafIDs {
		unchangedLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeafID)
		require.NoError(t, err)
		require.Empty(t, unchangedLeaf.KeyTweak)
	}
	unchangedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusSenderKeyTweaked, unchangedTransfer.Status)
}
