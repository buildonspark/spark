package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalProtoForReceiver(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	client := dbCtx.Client

	rng := rand.NewChaCha8([32]byte{1})
	senderPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverAPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverBPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	unrelatedPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransfer(t, ctx, rng, client, st.TransferStatusSenderKeyTweaked)

	receiverA, err := client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverAPub).
		SetStatus(st.TransferReceiverStatusSenderInitiated).
		Save(ctx)
	require.NoError(t, err)

	receiverB, err := client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverBPub).
		SetStatus(st.TransferReceiverStatusSenderInitiated).
		Save(ctx)
	require.NoError(t, err)

	tree := createTestTreeForClaim(t, ctx, senderPub, client)
	keyshare1 := createTestSigningKeyshare(t, ctx, rng, client)
	keyshare2 := createTestSigningKeyshare(t, ctx, rng, client)
	leafNodeA := createTestTreeNode(t, ctx, rng, client, tree, keyshare1)
	leafNodeB := createTestTreeNode(t, ctx, rng, client, tree, keyshare2)

	leafA := createTestTransferLeaf(t, ctx, client, transfer, leafNodeA)
	_, err = leafA.Update().SetTransferReceiverID(receiverA.ID).Save(ctx)
	require.NoError(t, err)

	leafB := createTestTransferLeaf(t, ctx, client, transfer, leafNodeB)
	_, err = leafB.Update().SetTransferReceiverID(receiverB.ID).Save(ctx)
	require.NoError(t, err)

	// Re-query transfer with edges needed by MarshalProtoForReceiver
	transfer, err = client.Transfer.Query().
		Where(enttransfer.ID(transfer.ID)).
		WithTransferReceivers().
		WithSparkInvoice().
		Only(ctx)
	require.NoError(t, err)

	t.Run("nil pubkey returns all leaves", func(t *testing.T) {
		proto, err := transfer.MarshalProtoForReceiver(ctx, nil)
		require.NoError(t, err)
		assert.Len(t, proto.Leaves, 2)
	})

	t.Run("receiver A pubkey returns only A leaves", func(t *testing.T) {
		proto, err := transfer.MarshalProtoForReceiver(ctx, &receiverAPub)
		require.NoError(t, err)
		require.Len(t, proto.Leaves, 1)
		assert.Equal(t, leafNodeA.ID.String(), proto.Leaves[0].Leaf.Id)
	})

	t.Run("receiver B pubkey returns only B leaves", func(t *testing.T) {
		proto, err := transfer.MarshalProtoForReceiver(ctx, &receiverBPub)
		require.NoError(t, err)
		require.Len(t, proto.Leaves, 1)
		assert.Equal(t, leafNodeB.ID.String(), proto.Leaves[0].Leaf.Id)
	})

	t.Run("unrelated pubkey returns all leaves", func(t *testing.T) {
		proto, err := transfer.MarshalProtoForReceiver(ctx, &unrelatedPub)
		require.NoError(t, err)
		assert.Len(t, proto.Leaves, 2)
	})

	t.Run("MarshalProto still returns all leaves", func(t *testing.T) {
		proto, err := transfer.MarshalProto(ctx)
		require.NoError(t, err)
		assert.Len(t, proto.Leaves, 2)
	})
}
