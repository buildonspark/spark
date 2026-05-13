package ent_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/stretchr/testify/require"
)

// Valid bitcoin transaction with a parseable encoded user timelock — same
// fixture used by other spark tests to satisfy the TransferLeaf hooks that
// parse intermediate_refund_tx.
const sampleRefundTxHex = "03000000000101d8966edeae1a3a05d0e5a3c971bb0a1b99bb901e76863812a40ea61fc60b87a000000000006c0700400214470000000000002251206b631936db9ab75c98e13235462f902944d9d81a45e3041bacaeec957bf7eeb700000000000000000451024e730140e06339a1f987b228843cf20f462f991264f89ca54c531c1c14d0df937d80acfd2ed9c626c6ad95106f3c9d90bc1de92b3d24aa89f03dd21974bb406e47ac84b000000000"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

// preloadedTransfer constructs a fully-populated transfer with two leaves
// split between two receivers. All inner edges (Leaf, Tree, SigningKeyshare,
// Parent) are pre-populated by hand so MarshalProto runs without any DB.
//
// Leaf 1 → receiver 1, value 700. Leaf 2 → receiver 2, value 300.
func preloadedTransfer(t *testing.T) (*ent.Transfer, keys.Public, keys.Public) {
	t.Helper()
	senderPub := keys.GeneratePrivateKey().Public()
	recv1Pub := keys.GeneratePrivateKey().Public()
	recv2Pub := keys.GeneratePrivateKey().Public()

	tree := &ent.Tree{ID: uuid.New(), Network: btcnetwork.Regtest}
	keyshare := &ent.SigningKeyshare{
		ID:           uuid.New(),
		PublicShares: map[string]keys.Public{"op": keys.GeneratePrivateKey().Public()},
		PublicKey:    keys.GeneratePrivateKey().Public(),
		MinSigners:   1,
	}
	// Stub parent so getParentNodeID short-circuits without a DB fallback.
	parent := &ent.TreeNode{ID: uuid.New(), Network: btcnetwork.Regtest}
	refundTx := mustHex(t, sampleRefundTxHex)

	makeLeafNode := func(value uint64, owner keys.Public) *ent.TreeNode {
		return &ent.TreeNode{
			ID:                  uuid.New(),
			Network:             btcnetwork.Regtest,
			Value:               value,
			VerifyingPubkey:     keys.GeneratePrivateKey().Public(),
			OwnerIdentityPubkey: owner,
			OwnerSigningPubkey:  keys.GeneratePrivateKey().Public(),
			RawTx:               refundTx,
			RawRefundTx:         refundTx,
			Status:              st.TreeNodeStatusAvailable,
			Edges: ent.TreeNodeEdges{
				Tree:            tree,
				SigningKeyshare: keyshare,
				Parent:          parent,
			},
		}
	}
	leaf1Node := makeLeafNode(700, recv1Pub)
	leaf2Node := makeLeafNode(300, recv2Pub)

	receiver1 := &ent.TransferReceiver{ID: uuid.New(), IdentityPubkey: recv1Pub}
	receiver2 := &ent.TransferReceiver{ID: uuid.New(), IdentityPubkey: recv2Pub}
	recv1ID, recv2ID := receiver1.ID, receiver2.ID

	transferLeaf1 := &ent.TransferLeaf{
		ID:                   uuid.New(),
		IntermediateRefundTx: refundTx,
		TransferReceiverID:   &recv1ID,
		Edges:                ent.TransferLeafEdges{Leaf: leaf1Node},
	}
	transferLeaf2 := &ent.TransferLeaf{
		ID:                   uuid.New(),
		IntermediateRefundTx: refundTx,
		TransferReceiverID:   &recv2ID,
		Edges:                ent.TransferLeafEdges{Leaf: leaf2Node},
	}

	now := time.Now()
	transfer := &ent.Transfer{
		ID:                     uuid.New(),
		SenderIdentityPubkey:   senderPub,
		ReceiverIdentityPubkey: recv1Pub,
		Network:                btcnetwork.Regtest,
		TotalValue:             1000,
		Status:                 st.TransferStatusReceiverKeyTweaked,
		Type:                   st.TransferTypeTransfer,
		ExpiryTime:             now.Add(time.Hour),
		CreateTime:             now,
		UpdateTime:             now,
		Edges: ent.TransferEdges{
			TransferLeaves:    []*ent.TransferLeaf{transferLeaf1, transferLeaf2},
			TransferReceivers: []*ent.TransferReceiver{receiver1, receiver2},
		},
	}
	return transfer, recv1Pub, recv2Pub
}

func TestMarshalProto_UsesPreloadedLeaves(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)

	proto, err := transfer.MarshalProto(t.Context())
	require.NoError(t, err)
	require.Len(t, proto.Leaves, 2)
	require.Equal(t, transfer.ID.String(), proto.Id)
	require.Equal(t, uint64(1000), proto.TotalValue)
	require.Len(t, proto.Receivers, 2)
}

func TestMarshalProtoForReceiver_PreloadedFiltersByReceiver(t *testing.T) {
	transfer, recv1Pub, recv2Pub := preloadedTransfer(t)

	proto1, err := transfer.MarshalProtoForReceiver(t.Context(), recv1Pub)
	require.NoError(t, err)
	require.Len(t, proto1.Leaves, 1)
	require.Equal(t, uint64(700), proto1.Leaves[0].Leaf.Value)

	proto2, err := transfer.MarshalProtoForReceiver(t.Context(), recv2Pub)
	require.NoError(t, err)
	require.Len(t, proto2.Leaves, 1)
	require.Equal(t, uint64(300), proto2.Leaves[0].Leaf.Value)
	require.NotEqual(t, proto1.Leaves[0].Leaf.Id, proto2.Leaves[0].Leaf.Id)
}

func TestMarshalProtoForReceiver_PreloadedReceiverNotInTransfer(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	stranger := keys.GeneratePrivateKey().Public()

	_, err := transfer.MarshalProtoForReceiver(t.Context(), stranger)
	require.Error(t, err)
}

// dbFixture seeds postgres with a Tree, TreeNode, and Transfer that has two
// TransferLeaves split across two receivers. Used to exercise the lazy-load
// fallback paths in MarshalProto / MarshalProtoForReceiver.
type dbFixture struct {
	transferID uuid.UUID
	recv1Pub   keys.Public
	recv2Pub   keys.Public
}

func seedTransferInDB(t *testing.T, ctx context.Context, client *ent.Client) dbFixture {
	t.Helper()

	senderPub := keys.GeneratePrivateKey().Public()
	recv1Pub := keys.GeneratePrivateKey().Public()
	recv2Pub := keys.GeneratePrivateKey().Public()
	ownerIdentity := keys.GeneratePrivateKey()
	verifyingKey := keys.GeneratePrivateKey()
	signingKey := keys.GeneratePrivateKey()
	secret := keys.GeneratePrivateKey()
	refundTx := mustHex(t, sampleRefundTxHex)

	tree, err := client.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetOwnerIdentityPubkey(ownerIdentity.Public()).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := client.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"1": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	makeNode := func() *ent.TreeNode {
		node, err := client.TreeNode.Create().
			SetID(uuid.New()).
			SetTree(tree).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(keyshare).
			SetValue(500).
			SetVerifyingPubkey(verifyingKey.Public()).
			SetOwnerIdentityPubkey(ownerIdentity.Public()).
			SetOwnerSigningPubkey(signingKey.Public()).
			SetRawTx(refundTx).
			SetRawRefundTx(refundTx).
			SetVout(0).
			SetStatus(st.TreeNodeStatusAvailable).
			Save(ctx)
		require.NoError(t, err)
		return node
	}
	node1 := makeNode()
	node2 := makeNode()

	transfer, err := client.Transfer.Create().
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(recv1Pub).
		SetStatus(st.TransferStatusReceiverKeyTweaked).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(time.Hour)).
		SetType(st.TransferTypeTransfer).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)

	receiver1, err := client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(recv1Pub).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	receiver2, err := client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(recv2Pub).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(node1).
		SetTransferReceiverID(receiver1.ID).
		SetPreviousRefundTx(refundTx).
		SetIntermediateRefundTx(refundTx).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(node2).
		SetTransferReceiverID(receiver2.ID).
		SetPreviousRefundTx(refundTx).
		SetIntermediateRefundTx(refundTx).
		Save(ctx)
	require.NoError(t, err)

	return dbFixture{transferID: transfer.ID, recv1Pub: recv1Pub, recv2Pub: recv2Pub}
}

func TestMarshalProto_LazyLoadsLeavesWhenNotPreloaded(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	fx := seedTransferInDB(t, ctx, dbCtx.Client)

	// Bare Get — TransferLeaves edge is NOT pre-loaded.
	transfer, err := dbCtx.Client.Transfer.Get(ctx, fx.transferID)
	require.NoError(t, err)
	require.Nil(t, transfer.Edges.TransferLeaves)

	proto, err := transfer.MarshalProto(ctx)
	require.NoError(t, err)
	require.Len(t, proto.Leaves, 2)
}

func TestMarshalProtoForReceiver_LazyLoadFiltersByReceiver(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	fx := seedTransferInDB(t, ctx, dbCtx.Client)

	// Pre-load TransferReceivers (required by MarshalProtoForReceiver) but
	// NOT TransferLeaves — exercises the lazy-load + SQL-side filter path.
	transfer, err := dbCtx.Client.Transfer.Query().
		Where(enttransfer.ID(fx.transferID)).
		WithTransferReceivers().
		Only(ctx)
	require.NoError(t, err)
	require.Nil(t, transfer.Edges.TransferLeaves)

	proto1, err := transfer.MarshalProtoForReceiver(ctx, fx.recv1Pub)
	require.NoError(t, err)
	require.Len(t, proto1.Leaves, 1)

	proto2, err := transfer.MarshalProtoForReceiver(ctx, fx.recv2Pub)
	require.NoError(t, err)
	require.Len(t, proto2.Leaves, 1)
	require.NotEqual(t, proto1.Leaves[0].Leaf.Id, proto2.Leaves[0].Leaf.Id)
}
