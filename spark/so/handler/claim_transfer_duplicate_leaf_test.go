package handler

import (
	rand "math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaimTransferTweakKeys_DuplicateLeafIDsRejected(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{31})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	leaf1 := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)
	leaf2 := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)

	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	transfer := createTestTransferForMIMO(
		t,
		ctx,
		sessionCtx.Client,
		senderPubKey,
		receiverPubKey,
		st.TransferStatusSenderKeyTweaked,
	)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		Save(ctx)
	require.NoError(t, err)

	_ = createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf1)
	_ = createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf2)

	handler := NewTransferHandler(sparktesting.TestConfig(t))

	tweakPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	duplicateLeafTweak := &pb.ClaimLeafKeyTweak{
		LeafId: leaf1.ID.String(),
		SecretShareTweak: &pb.SecretShare{
			SecretShare: make([]byte, 32),
			Proofs:      [][]byte{tweakPubKey.Serialize()},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": tweakPubKey.Serialize(),
		},
	}

	err = handler.ClaimTransferTweakKeys(ctx, &pb.ClaimTransferTweakKeysRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		LeavesToReceive: []*pb.ClaimLeafKeyTweak{
			duplicateLeafTweak,
			duplicateLeafTweak,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate leaf id")
}
