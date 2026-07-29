package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Access is filtered per row via checkTransferAccessMIMO; survivors marshal full.
func TestQueryTransfersByID_FiltersByAccessAndMarshalsFull(t *testing.T) {
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	viewer := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	otherSender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	otherReceiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Privacy-enabled wallets the viewer has no access to: the third transfer's
	// participants, so its access check fails.
	_, err = dbTx.WalletSetting.Create().SetOwnerIdentityPublicKey(otherSender).SetPrivateEnabled(true).Save(ctx)
	require.NoError(t, err)
	_, err = dbTx.WalletSetting.Create().SetOwnerIdentityPublicKey(otherReceiver).SetPrivateEnabled(true).Save(ctx)
	require.NoError(t, err)

	ctx = authn.InjectSessionForTests(ctx, viewer, 9999999999)
	tree := createTestTreeForClaim(t, ctx, viewer, dbTx)

	asSender := addByIDTestTransfer(t, ctx, rng, dbTx, tree, schematype.TransferStatusSenderInitiated, viewer, otherReceiver)
	asReceiver := addByIDTestTransfer(t, ctx, rng, dbTx, tree, schematype.TransferStatusSenderInitiated, otherSender, viewer)
	notAParticipant := addByIDTestTransfer(t, ctx, rng, dbTx, tree, schematype.TransferStatusSenderInitiated, otherSender, otherReceiver)

	handler := NewTransferHandler(cfg)
	resp, err := handler.QueryTransfersByID(ctx, &pb.QueryTransfersByIdRequest{
		TransferIds: []string{asSender.ID.String(), asReceiver.ID.String(), notAParticipant.ID.String()},
		Network:     pb.Network_REGTEST,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	transfersByID := make(map[string]*pb.Transfer)
	for _, tr := range resp.GetTransfers() {
		transfersByID[tr.GetId()] = tr
	}
	assert.Len(t, transfersByID, 2)
	assert.Contains(t, transfersByID, asSender.ID.String())
	assert.Contains(t, transfersByID, asReceiver.ID.String())
	assert.NotContains(t, transfersByID, notAParticipant.ID.String(), "caller is not a participant — must not be returned")

	// Full per-participant detail is emitted (not a receiver-projected view).
	assert.NotEmpty(t, transfersByID[asSender.ID.String()].GetSenders())
	assert.NotEmpty(t, transfersByID[asSender.ID.String()].GetReceivers())

	// A by-id fetch is a bounded, terminal set — no next page.
	assert.Equal(t, int64(-1), resp.GetOffset())
}

// Migration-critical: a completed transfer is still returned. Flashnet's current
// query_pending_transfers-with-ids pattern filters to pending, so it returns
// nothing once a transfer settles; the by-id endpoint does not.
func TestQueryTransfersByID_ReturnsRegardlessOfStatus(t *testing.T) {
	ctx, cfg := createTestContextForTransferQuery(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	viewer := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	ctx = authn.InjectSessionForTests(ctx, viewer, 9999999999)
	tree := createTestTreeForClaim(t, ctx, viewer, dbTx)

	completed := addByIDTestTransfer(t, ctx, rng, dbTx, tree, schematype.TransferStatusCompleted, sender, viewer)

	handler := NewTransferHandler(cfg)
	resp, err := handler.QueryTransfersByID(ctx, &pb.QueryTransfersByIdRequest{
		TransferIds: []string{completed.ID.String()},
		Network:     pb.Network_REGTEST,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTransfers(), 1)
	assert.Equal(t, completed.ID.String(), resp.GetTransfers()[0].GetId())
}

// Input validation lives in the proto validate.rules, enforced at the gRPC
// boundary by ValidationInterceptor — so assert the generated Validate().
func TestQueryTransfersByID_ValidatesInput(t *testing.T) {
	const validUUID = "11111111-1111-1111-1111-111111111111"
	tooMany := make([]string, maxTransferIDFilterValues+1)
	for i := range tooMany {
		tooMany[i] = validUUID
	}

	tests := []struct {
		name        string
		req         *pb.QueryTransfersByIdRequest
		expectedErr bool
	}{
		{"valid", &pb.QueryTransfersByIdRequest{TransferIds: []string{validUUID}, Network: pb.Network_REGTEST}, false},
		{"empty transfer_ids", &pb.QueryTransfersByIdRequest{Network: pb.Network_REGTEST}, true},
		{"unspecified network", &pb.QueryTransfersByIdRequest{TransferIds: []string{validUUID}}, true},
		{"out-of-range network", &pb.QueryTransfersByIdRequest{TransferIds: []string{validUUID}, Network: pb.Network(99)}, true},
		{"too many transfer_ids", &pb.QueryTransfersByIdRequest{TransferIds: tooMany, Network: pb.Network_REGTEST}, true},
		{"malformed transfer id", &pb.QueryTransfersByIdRequest{TransferIds: []string{"not-a-uuid"}, Network: pb.Network_REGTEST}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.expectedErr {
				require.Error(t, tc.req.Validate())
			} else {
				require.NoError(t, tc.req.Validate())
			}
		})
	}
}

// addByIDTestTransfer creates a transfer with MIMO sender/receiver edges and a
// leaf, matching the fixture used by the by-transfer-id query tests.
func addByIDTestTransfer(t *testing.T, ctx context.Context, rng *rand.ChaCha8, dbTx *ent.Client, tree *ent.Tree, txStatus schematype.TransferStatus, sender, receiver keys.Public) *ent.Transfer {
	t.Helper()
	transfer, err := dbTx.Transfer.Create().
		SetType(schematype.TransferTypeTransfer).
		SetStatus(txStatus).
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
	leaf := createTestTreeNodeForTransferQuery(t, ctx, rng, dbTx, tree, receiver)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createOldBitcoinTxBytes(t, receiver)).
		SetIntermediateRefundTx(createOldBitcoinTxBytes(t, receiver)).
		Save(ctx)
	require.NoError(t, err)
	return transfer
}
