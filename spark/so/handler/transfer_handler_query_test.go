package handler

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	}

	// Call queryTransfers with isSSP=true, isPending=false
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
	}

	// Call queryTransfers with isSSP=true, isPending=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, true)

	// Should not error - SSP bypasses authz check
	require.NoError(t, err, "SSP should be able to query transfers without auth")
	assert.NotNil(t, resp, "Response should not be nil")
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
	}

	// Call queryTransfers with isPending=false, isSSP=false
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
	}

	// Call queryTransfers with isPending=false, isSSP=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)

	// Should return empty response (not error) when session doesn't have access
	require.NoError(t, err, "Should not error when session doesn't have access, should return empty response")
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Transfers, "Should return empty transfers when viewer doesn't have access")
	assert.Equal(t, int64(-1), resp.Offset, "Offset should be -1 when no access")
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
	}

	// Call queryTransfers with isPending=false, isSSP=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)

	// Should return empty response (not error) when no session - HasReadAccessToWallet returns false (no access)
	require.NoError(t, err, "Should not error when there's no session, should return empty response")
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Transfers, "Should return empty transfers when no session")
	assert.Equal(t, int64(-1), resp.Offset, "Offset should be -1 when no access")
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
