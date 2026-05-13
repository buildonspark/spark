package handler

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/entexample"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestQueryTransfers_SSP_WithReceiverFilter(t *testing.T) {
	// Test that SSP can query transfers by receiver without authorization check
	ctx, cfg := createTestContextForTransferQuery(t)

	// Create receiver identity key
	receiverIDPubKey := keys.GeneratePrivateKey().Public()

	// Create a transfer filter with receiver identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: receiverIDPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
	}

	// Call queryTransfers with isSSP=true, pendingOnly=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, true)

	// Should not error - SSP bypasses authz check
	require.NoError(t, err, "SSP should be able to query transfers without auth")
	assert.NotNil(t, resp, "Response should not be nil")
}

func TestQueryTransfers_SSP_WithSenderFilter(t *testing.T) {
	// Test that SSP can query transfers by sender without authorization check
	ctx, cfg := createTestContextForTransferQuery(t)

	// Create sender identity key
	senderIDPubKey := keys.GeneratePrivateKey().Public()

	// Create a transfer filter with sender identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderIdentityPublicKey{
			SenderIdentityPublicKey: senderIDPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
	}

	// Call queryTransfers with isSSP=true, pendingOnly=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, true)

	// Should not error - SSP bypasses authz check
	require.NoError(t, err, "SSP should be able to query transfers without auth")
	assert.NotNil(t, resp, "Response should not be nil")
}

func TestQueryTransfersRejectsTooManyTransferIDsBeforeParsing(t *testing.T) {
	ctx, cfg := createTestContextForTransferQuery(t)
	filter := &pb.TransferFilter{
		TransferIds: make([]string, maxTransferIDFilterValues+1),
		Network:     pb.Network_REGTEST,
	}

	resp, err := NewTransferHandler(cfg).queryTransfers(ctx, filter, false, false)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "transfer ids provided")
}

func TestQueryTransfers_NotSSP_RequiresAuthz(t *testing.T) {
	// Test that non-SSP queries require authentication and match participant
	ctx, cfg := createTestContextForTransferQuery(t)

	// Create identity keys
	receiverIDPubKey := keys.GeneratePrivateKey().Public()

	// Inject session for the receiver
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(receiverIDPubKey.Serialize()), 9999999999)

	// Create a transfer filter with receiver identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: receiverIDPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
	}

	// Call queryTransfers with pendingOnly=false, isSSP=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)

	// Should not error - session matches receiver
	require.NoError(t, err, "Should be able to query transfers when session matches participant")
	assert.NotNil(t, resp, "Response should not be nil")
}

func TestQueryTransfers_NotSSP_RequiresAuthz_Mismatch(t *testing.T) {
	// Test that non-SSP queries return empty response when session doesn't have access to participant wallet
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create identity keys
	receiverIDPubKey := keys.GeneratePrivateKey().Public()
	differentIDPubKey := keys.GeneratePrivateKey().Public()

	// Create wallet setting with privacy enabled for the receiver
	// This ensures HasReadAccessToWallet returns false when session doesn't match
	_, err = dbTx.WalletSetting.Create().
		SetOwnerIdentityPublicKey(receiverIDPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	// Inject knobs to enable privacy feature
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100, // 100% rollout = always enabled
	})
	ctx = knobs.InjectKnobsService(ctx, fixedKnobs)

	// Inject session for a different identity (not the receiver)
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(differentIDPubKey.Serialize()), 9999999999)

	// Create a transfer filter with receiver identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: receiverIDPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
	}

	// Call queryTransfers with pendingOnly=false, isSSP=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)

	// Should return empty response (not error) when session doesn't have access
	require.NoError(t, err, "Should not error when session doesn't have access, should return empty response")
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Transfers, "Should return empty transfers when viewer doesn't have access")
	assert.Equal(t, int64(-1), resp.Offset, "Offset should be -1 when no access")
}

func TestQueryTransfersRejectsMalformedFiltersWithTypedErrors(t *testing.T) {
	ctx, cfg := createTestContextForTransferQuery(t)
	handler := NewTransferHandler(cfg)
	identityKey := keys.GeneratePrivateKey().Public().Serialize()

	base := func() *pb.TransferFilter {
		return &pb.TransferFilter{
			Network: pb.Network_REGTEST,
			Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
				ReceiverIdentityPublicKey: identityKey,
			},
		}
	}

	tests := []struct {
		name        string
		filter      *pb.TransferFilter
		pendingOnly bool
	}{
		{
			name: "empty receiver identity public key",
			filter: &pb.TransferFilter{
				Network: pb.Network_REGTEST,
				Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
					ReceiverIdentityPublicKey: nil,
				},
			},
		},
		{
			name: "malformed sender identity public key",
			filter: &pb.TransferFilter{
				Network: pb.Network_REGTEST,
				Participant: &pb.TransferFilter_SenderIdentityPublicKey{
					SenderIdentityPublicKey: []byte{0x02, 0x01},
				},
			},
		},
		{
			name: "malformed sender or receiver identity public key",
			filter: &pb.TransferFilter{
				Network: pb.Network_REGTEST,
				Participant: &pb.TransferFilter_SenderOrReceiverIdentityPublicKey{
					SenderOrReceiverIdentityPublicKey: []byte{0x02, 0x01},
				},
			},
		},
		{
			name: "malformed transfer id",
			filter: &pb.TransferFilter{
				Network:     pb.Network_REGTEST,
				TransferIds: []string{"not-a-uuid"},
			},
		},
		{
			name: "invalid transfer status",
			filter: func() *pb.TransferFilter {
				filter := base()
				filter.Statuses = []pb.TransferStatus{pb.TransferStatus(999)}
				return filter
			}(),
		},
		{
			name: "statuses on pending query",
			filter: func() *pb.TransferFilter {
				filter := base()
				filter.Statuses = []pb.TransferStatus{pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED}
				return filter
			}(),
			pendingOnly: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := handler.queryTransfers(ctx, tt.filter, tt.pendingOnly, true)
			require.Nil(t, resp)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestQueryTransfers_NotSSP_NoSession(t *testing.T) {
	// Test that non-SSP queries fail when there's no session
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create identity keys
	receiverIDPubKey := keys.GeneratePrivateKey().Public()

	// Create wallet setting with privacy enabled for the receiver
	// This ensures HasReadAccessToWallet will check for session
	_, err = dbTx.WalletSetting.Create().
		SetOwnerIdentityPublicKey(receiverIDPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	// Inject knobs to enable privacy feature
	// This ensures the privacy check actually runs
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100, // 100% rollout = always enabled
	})
	ctx = knobs.InjectKnobsService(ctx, fixedKnobs)

	// Don't inject any session

	// Create a transfer filter with receiver identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: receiverIDPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
	}

	// Call queryTransfers with pendingOnly=false, isSSP=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)

	// Should return empty response (not error) when no session - HasReadAccessToWallet returns false (no access)
	require.NoError(t, err, "Should not error when there's no session, should return empty response")
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Transfers, "Should return empty transfers when no session")
	assert.Equal(t, int64(-1), resp.Offset, "Offset should be -1 when no access")
}

func TestQueryTransfersRejectsMalformedPagination(t *testing.T) {
	ctx := t.Context()
	handler := NewTransferHandler(&so.Config{})

	validFilter := func() *pb.TransferFilter {
		return &pb.TransferFilter{
			Participant: &pb.TransferFilter_SenderIdentityPublicKey{
				SenderIdentityPublicKey: []byte{1},
			},
			Network: pb.Network_REGTEST,
			Limit:   10,
			Offset:  0,
		}
	}

	t.Run("nil filter uses existing selector validation", func(t *testing.T) {
		resp, err := handler.QueryAllTransfers(ctx, nil, false)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "must specify either filter.Participant or filter.TransferIds")
	})

	t.Run("negative limit", func(t *testing.T) {
		filter := validFilter()
		filter.Limit = -1

		resp, err := handler.QueryAllTransfers(ctx, filter, false)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "limit must be non-negative")
	})

	t.Run("negative offset", func(t *testing.T) {
		filter := validFilter()
		filter.Offset = -1

		resp, err := handler.QueryAllTransfers(ctx, filter, false)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "offset must be non-negative")
	})

	t.Run("pending transfers negative limit", func(t *testing.T) {
		filter := validFilter()
		filter.Limit = -1

		resp, err := handler.QueryPendingTransfers(ctx, filter)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "limit must be non-negative")
	})
}

// Helper function to create test context with authz enabled
func createTestContextForTransferQuery(t *testing.T) (context.Context, *so.Config) {
	ctx, _ := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	cfg.AuthzEnforced = true // Enable authz enforcement for these tests
	return ctx, cfg
}

// createTestTreeNodeForTransferQuery creates a TreeNode for transfer query tests
func createTestTreeNodeForTransferQuery(t *testing.T, ctx context.Context, rng *rand.ChaCha8, dbTx *ent.Client, tree *ent.Tree, ownerPubKey keys.Public) *ent.TreeNode {
	keyshare, err := dbTx.SigningKeyshare.Create().
		SetStatus(schematype.KeyshareStatusAvailable).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"test": keys.MustGeneratePrivateKeyFromRand(rng).Public()}).
		SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create valid transaction bytes
	validTxBytes := createOldBitcoinTxBytes(t, ownerPubKey)

	node, err := dbTx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetStatus(schematype.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerPubKey).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetValue(100000).
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetSigningKeyshare(keyshare).
		SetRawTx(validTxBytes).
		SetRawRefundTx(validTxBytes).
		SetDirectTx(validTxBytes).
		SetDirectRefundTx(validTxBytes).
		SetDirectFromCpfpRefundTx(validTxBytes).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)
	return node
}

func TestQueryTransfers_WithTransferIds_AccessCheck(t *testing.T) {
	// Test that when using TransferIds filter, checkTransferAccess filters transfers based on sender/receiver access
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	viewerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Enable privacy knob so HasReadAccessToWallet actually checks access
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100, // 100% rollout = always enabled
	})
	ctx = knobs.InjectKnobsService(ctx, fixedKnobs)

	// Create wallet settings with privacy enabled for sender and receiver
	// This ensures HasReadAccessToWallet returns false when viewer doesn't match
	_, err = dbTx.WalletSetting.Create().
		SetOwnerIdentityPublicKey(senderIdentityPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = dbTx.WalletSetting.Create().
		SetOwnerIdentityPublicKey(receiverIdentityPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	// Inject session for the viewer
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(viewerIdentityPubKey.Serialize()), 9999999999)

	// Create a tree for network filtering
	tree := createTestTreeForClaim(t, ctx, viewerIdentityPubKey, dbTx)

	// Create transfers:
	// 1. Viewer is sender - should be visible
	transfer1, err := dbTx.Transfer.Create().
		SetType(schematype.TransferTypeTransfer).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetSenderIdentityPubkey(viewerIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetNetwork(tree.Network).
		Save(ctx)
	require.NoError(t, err)
	leaf1 := createTestTreeNodeForTransferQuery(t, ctx, rng, dbTx, tree, receiverIdentityPubKey)
	// Create valid transaction bytes for refund transactions
	previousRefundTxBytes := createOldBitcoinTxBytes(t, receiverIdentityPubKey)
	intermediateRefundTxBytes := createOldBitcoinTxBytes(t, receiverIdentityPubKey)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer1).
		SetLeaf(leaf1).
		SetPreviousRefundTx(previousRefundTxBytes).
		SetIntermediateRefundTx(intermediateRefundTxBytes).
		Save(ctx)
	require.NoError(t, err)

	// 2. Viewer is receiver - should be visible
	transfer2, err := dbTx.Transfer.Create().
		SetType(schematype.TransferTypeTransfer).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(viewerIdentityPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetNetwork(tree.Network).
		Save(ctx)
	require.NoError(t, err)
	leaf2 := createTestTreeNodeForTransferQuery(t, ctx, rng, dbTx, tree, viewerIdentityPubKey)
	// Create valid transaction bytes for refund transactions
	previousRefundTxBytes2 := createOldBitcoinTxBytes(t, viewerIdentityPubKey)
	intermediateRefundTxBytes2 := createOldBitcoinTxBytes(t, viewerIdentityPubKey)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer2).
		SetLeaf(leaf2).
		SetPreviousRefundTx(previousRefundTxBytes2).
		SetIntermediateRefundTx(intermediateRefundTxBytes2).
		Save(ctx)
	require.NoError(t, err)

	// 3. Viewer is neither sender nor receiver - should NOT be visible
	transfer3, err := dbTx.Transfer.Create().
		SetType(schematype.TransferTypeTransfer).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetNetwork(tree.Network).
		Save(ctx)
	require.NoError(t, err)
	leaf3 := createTestTreeNodeForTransferQuery(t, ctx, rng, dbTx, tree, receiverIdentityPubKey)
	// Create valid transaction bytes for refund transactions
	previousRefundTxBytes3 := createOldBitcoinTxBytes(t, receiverIdentityPubKey)
	intermediateRefundTxBytes3 := createOldBitcoinTxBytes(t, receiverIdentityPubKey)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer3).
		SetLeaf(leaf3).
		SetPreviousRefundTx(previousRefundTxBytes3).
		SetIntermediateRefundTx(intermediateRefundTxBytes3).
		Save(ctx)
	require.NoError(t, err)

	// Query with TransferIds filter
	filter := &pb.TransferFilter{
		Participant: nil, // No participant filter
		TransferIds: []string{
			transfer1.ID.String(),
			transfer2.ID.String(),
			transfer3.ID.String(),
		},
		Network: pb.Network_REGTEST,
	}

	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Should only return transfers 1 and 2 (where viewer is sender or receiver)
	assert.Len(t, resp.Transfers, 2, "Should only return transfers where viewer has access")
	transferIDs := make(map[string]bool)
	for _, t := range resp.Transfers {
		transferIDs[t.Id] = true
	}
	assert.True(t, transferIDs[transfer1.ID.String()], "Transfer1 (viewer is sender) should be included")
	assert.True(t, transferIDs[transfer2.ID.String()], "Transfer2 (viewer is receiver) should be included")
	assert.False(t, transferIDs[transfer3.ID.String()], "Transfer3 (viewer is neither) should NOT be included")
}

func TestQueryTransfers_WithTransferIds_MasterKeyAccess(t *testing.T) {
	// Test that when using TransferIds filter, checkTransferAccess allows access when viewer is master key
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	masterIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	walletOwnerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create wallet setting where master is the viewer
	_, err = dbTx.WalletSetting.Create().
		SetOwnerIdentityPublicKey(walletOwnerIdentityPubKey).
		SetPrivateEnabled(true).
		SetMasterIdentityPublicKey(masterIdentityPubKey).
		Save(ctx)
	require.NoError(t, err)

	// Inject session for the master (viewer)
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(masterIdentityPubKey.Serialize()), 9999999999)

	// Create a tree for network filtering
	tree := createTestTreeForClaim(t, ctx, walletOwnerIdentityPubKey, dbTx)

	// Create tree node
	leaf := createTestTreeNodeForTransferQuery(t, ctx, rng, dbTx, tree, walletOwnerIdentityPubKey)

	// Create transfer where receiver is the wallet owned by master
	transfer, err := dbTx.Transfer.Create().
		SetType(schematype.TransferTypeTransfer).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetSenderIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetReceiverIdentityPubkey(walletOwnerIdentityPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetNetwork(tree.Network).
		Save(ctx)
	require.NoError(t, err)

	// Create valid transaction bytes for refund transactions
	previousRefundTxBytes := createOldBitcoinTxBytes(t, walletOwnerIdentityPubKey)
	intermediateRefundTxBytes := createOldBitcoinTxBytes(t, walletOwnerIdentityPubKey)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(previousRefundTxBytes).
		SetIntermediateRefundTx(intermediateRefundTxBytes).
		Save(ctx)
	require.NoError(t, err)

	// Query with TransferIds filter
	filter := &pb.TransferFilter{
		Participant: nil, // No participant filter
		TransferIds: []string{transfer.ID.String()},
		Network:     pb.Network_REGTEST,
	}

	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Should return the transfer because master has access to the receiver wallet
	assert.Len(t, resp.Transfers, 1, "Should return transfer where master has access to receiver wallet")
	assert.Equal(t, transfer.ID.String(), resp.Transfers[0].Id)
}

// TestQueryTransfers_WithTransferIds_AccessCheck_MIMO verifies that when the MIMO query-transfers knob is on,
// queryTransfers uses checkTransferAccessMIMO (sender/receiver from edges) and still filters by viewer access.
func TestQueryTransfers_WithTransferIds_AccessCheck_MIMO(t *testing.T) {
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	viewerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled:                  100,
		knobs.KnobReadMIMODataModelQueryTransfers: 100,
	})
	ctx = knobs.InjectKnobsService(ctx, fixedKnobs)

	_, err = dbTx.WalletSetting.Create().
		SetOwnerIdentityPublicKey(senderIdentityPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = dbTx.WalletSetting.Create().
		SetOwnerIdentityPublicKey(receiverIdentityPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(viewerIdentityPubKey.Serialize()), 9999999999)

	tree := createTestTreeForClaim(t, ctx, viewerIdentityPubKey, dbTx)

	addTransferWithMIMOEdges := func(sender, receiver keys.Public) *ent.Transfer {
		transfer, err := dbTx.Transfer.Create().
			SetType(schematype.TransferTypeTransfer).
			SetStatus(schematype.TransferStatusSenderInitiated).
			SetSenderIdentityPubkey(sender).
			SetReceiverIdentityPubkey(receiver).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetNetwork(tree.Network).
			Save(ctx)
		require.NoError(t, err)
		_, err = dbTx.TransferSender.Create().SetTransferID(transfer.ID).SetIdentityPubkey(sender).SetTransferType(transfer.Type).Save(ctx)
		require.NoError(t, err)
		_, err = dbTx.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiver).
			SetStatus(schematype.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)
		return transfer
	}

	transfer1 := addTransferWithMIMOEdges(viewerIdentityPubKey, receiverIdentityPubKey)
	leaf1 := createTestTreeNodeForTransferQuery(t, ctx, rng, dbTx, tree, receiverIdentityPubKey)
	prevRefund1 := createOldBitcoinTxBytes(t, receiverIdentityPubKey)
	interRefund1 := createOldBitcoinTxBytes(t, receiverIdentityPubKey)
	_, err = dbTx.TransferLeaf.Create().SetTransfer(transfer1).SetLeaf(leaf1).SetPreviousRefundTx(prevRefund1).SetIntermediateRefundTx(interRefund1).Save(ctx)
	require.NoError(t, err)

	transfer2 := addTransferWithMIMOEdges(senderIdentityPubKey, viewerIdentityPubKey)
	leaf2 := createTestTreeNodeForTransferQuery(t, ctx, rng, dbTx, tree, viewerIdentityPubKey)
	prevRefund2 := createOldBitcoinTxBytes(t, viewerIdentityPubKey)
	interRefund2 := createOldBitcoinTxBytes(t, viewerIdentityPubKey)
	_, err = dbTx.TransferLeaf.Create().SetTransfer(transfer2).SetLeaf(leaf2).SetPreviousRefundTx(prevRefund2).SetIntermediateRefundTx(interRefund2).Save(ctx)
	require.NoError(t, err)

	transfer3 := addTransferWithMIMOEdges(senderIdentityPubKey, receiverIdentityPubKey)
	leaf3 := createTestTreeNodeForTransferQuery(t, ctx, rng, dbTx, tree, receiverIdentityPubKey)
	prevRefund3 := createOldBitcoinTxBytes(t, receiverIdentityPubKey)
	interRefund3 := createOldBitcoinTxBytes(t, receiverIdentityPubKey)
	_, err = dbTx.TransferLeaf.Create().SetTransfer(transfer3).SetLeaf(leaf3).SetPreviousRefundTx(prevRefund3).SetIntermediateRefundTx(interRefund3).Save(ctx)
	require.NoError(t, err)

	filter := &pb.TransferFilter{
		TransferIds: []string{transfer1.ID.String(), transfer2.ID.String(), transfer3.ID.String()},
		Network:     pb.Network_REGTEST,
	}

	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Transfers, 2)
	received := make(map[string]bool)
	for _, tr := range resp.Transfers {
		received[tr.Id] = true
	}
	assert.True(t, received[transfer1.ID.String()])
	assert.True(t, received[transfer2.ID.String()])
	assert.False(t, received[transfer3.ID.String()])
}

// createTestTransferWithTime creates a transfer with a specific create time for testing time filters
func createTestTransferWithTime(t *testing.T, ctx context.Context, dbTx *ent.Client, createTime time.Time, identityPubKey keys.Public, network btcnetwork.Network) *ent.Transfer {
	transfer, err := dbTx.Transfer.Create().
		SetSenderIdentityPubkey(identityPubKey).
		SetReceiverIdentityPubkey(identityPubKey).
		SetNetwork(network).
		SetTotalValue(1000).
		SetStatus(schematype.TransferStatusCompleted).
		SetType(schematype.TransferTypeTransfer).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetCreateTime(createTime). // Override create time
		Save(ctx)
	require.NoError(t, err)
	return transfer
}

func TestQueryTransfers_WithCreatedAfterFilter(t *testing.T) {
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create test transfers at different times
	now := time.Now()
	past := now.Add(-24 * time.Hour)
	future := now.Add(24 * time.Hour)

	// Create transfers with different create times
	transfer1 := createTestTransferWithTime(t, ctx, dbTx, past, identityPubKey, btcnetwork.Regtest)
	transfer2 := createTestTransferWithTime(t, ctx, dbTx, now, identityPubKey, btcnetwork.Regtest)
	transfer3 := createTestTransferWithTime(t, ctx, dbTx, future, identityPubKey, btcnetwork.Regtest)

	// Query transfers created strictly after 'now' (exclusive)
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderOrReceiverIdentityPublicKey{
			SenderOrReceiverIdentityPublicKey: identityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		TimeFilter: &pb.TransferFilter_CreatedAfter{
			CreatedAfter: timestamppb.New(now),
		},
	}

	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, true)
	require.NoError(t, err)
	require.Len(t, resp.Transfers, 1) // Only transfer3 (future)

	// Verify correct transfers returned (transfer2 at 'now' is excluded - exclusive filter)
	assert.Equal(t, transfer3.ID.String(), resp.Transfers[0].Id)
	transferIds := []string{resp.Transfers[0].Id}
	assert.NotContains(t, transferIds, transfer1.ID.String())
	assert.NotContains(t, transferIds, transfer2.ID.String())
}

func TestQueryTransfers_WithCreatedBeforeFilter(t *testing.T) {
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Use fixed times to avoid any edge cases with time precision
	baseTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	oldTransfer := createTestTransferWithTime(t, ctx, dbTx, baseTime.Add(-48*time.Hour), identityPubKey, btcnetwork.Regtest)
	middleTransfer := createTestTransferWithTime(t, ctx, dbTx, baseTime.Add(-24*time.Hour), identityPubKey, btcnetwork.Regtest)
	recentTransfer := createTestTransferWithTime(t, ctx, dbTx, baseTime.Add(-1*time.Hour), identityPubKey, btcnetwork.Regtest)
	futureTransfer := createTestTransferWithTime(t, ctx, dbTx, baseTime.Add(24*time.Hour), identityPubKey, btcnetwork.Regtest)

	// Query transfers created strictly before baseTime (exclusive)
	// Should return oldTransfer and middleTransfer (both before baseTime)
	// Should NOT return recentTransfer or futureTransfer (both at or after baseTime)
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderOrReceiverIdentityPublicKey{
			SenderOrReceiverIdentityPublicKey: identityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		TimeFilter: &pb.TransferFilter_CreatedBefore{
			CreatedBefore: timestamppb.New(baseTime.Add(-12 * time.Hour)), // Cutoff at -12h
		},
	}

	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, true)
	require.NoError(t, err)
	require.Len(t, resp.Transfers, 2, "Should return exactly 2 transfers (oldTransfer and middleTransfer)")

	// Verify correct transfers returned
	transferIds := make(map[string]bool)
	for _, transfer := range resp.Transfers {
		transferIds[transfer.Id] = true
	}
	assert.True(t, transferIds[oldTransfer.ID.String()], "oldTransfer (-48h) should be included")
	assert.True(t, transferIds[middleTransfer.ID.String()], "middleTransfer (-24h) should be included")
	assert.False(t, transferIds[recentTransfer.ID.String()], "recentTransfer (-1h) should NOT be included")
	assert.False(t, transferIds[futureTransfer.ID.String()], "futureTransfer (+24h) should NOT be included")
}

func TestQueryTransfers_FilterSSPCounterSwapAsTransfer(t *testing.T) {
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		fmt.Sprintf("%s@%s", knobs.KnobFilterSSPCounterSwapAsTransfer, btcnetwork.Regtest.String()): 100,
	})
	ctx = knobs.InjectKnobsService(ctx, fixedKnobs)

	rng := rand.NewChaCha8([32]byte{})
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	network := btcnetwork.Regtest

	randomTransfer := createTestTransferWithTime(t, ctx, dbTx, time.Now(), userIdentityPubKey, network)
	require.NoError(t, err)

	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderOrReceiverIdentityPublicKey{
			SenderOrReceiverIdentityPublicKey: userIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Types: []pb.TransferType{
			pb.TransferType_TRANSFER,
		},
	}

	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, true)

	// Should handle NotFound error when no swap is found
	require.NoError(t, err)
	require.Len(t, resp.Transfers, 1)
	assert.Equal(t, randomTransfer.ID.String(), resp.Transfers[0].Id)

	entexample.NewTransferExample(t, dbTx).
		SetSenderIdentityPubkey(userIdentityPubKey).
		SetReceiverIdentityPubkey(sspIdentityPubKey).
		SetType(schematype.TransferTypeSwap).
		MustExec(ctx)

	counterSwapTransfer := entexample.NewTransferExample(t, dbTx).
		SetSenderIdentityPubkey(sspIdentityPubKey).
		SetReceiverIdentityPubkey(userIdentityPubKey).
		SetType(schematype.TransferTypeTransfer).
		MustExec(ctx)

	// Should not return the SSP transfer
	resp, err = handler.queryTransfers(ctx, filter, false, true)

	require.NoError(t, err)
	require.Len(t, resp.Transfers, 1)
	assert.Equal(t, randomTransfer.ID.String(), resp.Transfers[0].Id)

	filter.Types = []pb.TransferType{
		pb.TransferType_TRANSFER,
		pb.TransferType_COUNTER_SWAP_V3,
		pb.TransferType_COUNTER_SWAP,
	}

	// Since we're requesting counter swaps, should return the SSP transfer
	resp, err = handler.queryTransfers(ctx, filter, false, true)
	require.NoError(t, err)
	require.Len(t, resp.Transfers, 2)
	assert.Equal(t, counterSwapTransfer.ID.String(), resp.Transfers[0].Id)
	assert.Equal(t, randomTransfer.ID.String(), resp.Transfers[1].Id)
}
