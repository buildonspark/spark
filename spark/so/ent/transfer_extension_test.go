package ent_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// mimoOnContext returns a context with KnobReadMIMOMultiParticipantFormat=100,
// so MarshalProto emits Senders[]/Receivers[]. Mirrors how prod runs today.
func mimoOnContext(t *testing.T) context.Context {
	t.Helper()
	return knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobReadMIMOMultiParticipantFormat: 100,
	}))
}

// mimoOnContextWithLogObserver returns a knob-on context plus an observer for
// captured log entries, so tests can assert on the missing-edge warnings.
func mimoOnContextWithLogObserver(t *testing.T) (context.Context, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.WarnLevel)
	ctx := logging.Inject(t.Context(), zap.New(core))
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobReadMIMOMultiParticipantFormat: 100,
	}))
	return ctx, logs
}

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

	receiver1 := &ent.TransferReceiver{ID: uuid.New(), IdentityPubkey: recv1Pub, Status: st.TransferReceiverStatusKeyTweaked}
	receiver2 := &ent.TransferReceiver{ID: uuid.New(), IdentityPubkey: recv2Pub, Status: st.TransferReceiverStatusKeyTweaked}
	senderID := uuid.New()
	sender := &ent.TransferSender{ID: senderID, IdentityPubkey: senderPub}

	transferLeaf1 := &ent.TransferLeaf{
		ID:                   uuid.New(),
		IntermediateRefundTx: refundTx,
		TransferReceiverID:   new(receiver1.ID),
		TransferSenderID:     &senderID,
		Edges:                ent.TransferLeafEdges{Leaf: leaf1Node},
	}
	transferLeaf2 := &ent.TransferLeaf{
		ID:                   uuid.New(),
		IntermediateRefundTx: refundTx,
		TransferReceiverID:   new(receiver2.ID),
		TransferSenderID:     &senderID,
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
			TransferSenders:   []*ent.TransferSender{sender},
		},
	}
	return transfer, recv1Pub, recv2Pub
}

func TestMarshalProto_UsesPreloadedLeaves(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)

	proto, err := transfer.MarshalProto(mimoOnContext(t))
	require.NoError(t, err)
	require.Len(t, proto.Leaves, 2)
	require.Equal(t, transfer.ID.String(), proto.Id)
	require.Equal(t, uint64(1000), proto.TotalValue)
	require.Len(t, proto.Receivers, 2)
}

func TestMarshalProtoForReceiver_PreloadedFiltersByReceiver(t *testing.T) {
	transfer, recv1Pub, recv2Pub := preloadedTransfer(t)
	ctx := mimoOnContext(t)

	proto1, err := transfer.MarshalProtoForReceiver(ctx, recv1Pub)
	require.NoError(t, err)
	require.Len(t, proto1.Leaves, 1)
	require.Equal(t, uint64(700), proto1.Leaves[0].Leaf.Value)

	proto2, err := transfer.MarshalProtoForReceiver(ctx, recv2Pub)
	require.NoError(t, err)
	require.Len(t, proto2.Leaves, 1)
	require.Equal(t, uint64(300), proto2.Leaves[0].Leaf.Value)
	require.NotEqual(t, proto1.Leaves[0].Leaf.Id, proto2.Leaves[0].Leaf.Id)
}

func TestMarshalProtoForReceiver_PreloadedReceiverNotInTransfer(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	stranger := keys.GeneratePrivateKey().Public()

	_, err := transfer.MarshalProtoForReceiver(mimoOnContext(t), stranger)
	require.Error(t, err)
}

func TestMarshalProto_PopulatesSenders(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	senderID := uuid.New()
	transfer.Edges.TransferSenders = []*ent.TransferSender{
		{ID: senderID, IdentityPubkey: transfer.SenderIdentityPubkey},
	}

	proto, err := transfer.MarshalProto(mimoOnContext(t))
	require.NoError(t, err)
	require.Len(t, proto.Senders, 1, "expected one TransferSender")
	require.Equal(t, transfer.SenderIdentityPubkey.Serialize(), proto.Senders[0].IdentityPublicKey)
	require.Equal(t, senderID.String(), proto.Senders[0].Id)
}

func TestMarshalProto_PopulatesReceiverIDAndCompletionTime(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	completion := time.Now().UTC().Truncate(time.Second)
	transfer.Edges.TransferReceivers[0].Status = st.TransferReceiverStatusCompleted
	transfer.Edges.TransferReceivers[0].CompletionTime = completion

	proto, err := transfer.MarshalProto(mimoOnContext(t))
	require.NoError(t, err)
	require.Len(t, proto.Receivers, 2)

	// receivers[] iteration mirrors edge order; first entry has CompletionTime.
	var completed *pb.TransferReceiver
	var pending *pb.TransferReceiver
	for _, r := range proto.Receivers {
		if r.Id == transfer.Edges.TransferReceivers[0].ID.String() {
			completed = r
		} else {
			pending = r
		}
	}
	require.NotNil(t, completed)
	require.NotNil(t, pending)
	require.Equal(t, completion.Unix(), completed.CompletionTime.AsTime().Unix())
	require.Nil(t, pending.CompletionTime, "non-completed receiver should have nil completion_time")
}

func TestMarshalProto_EmitsReceiverWithoutLeaves(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	transfer.Edges.TransferLeaves = []*ent.TransferLeaf{}

	proto, err := transfer.MarshalProto(mimoOnContext(t))
	require.NoError(t, err)
	require.Len(t, proto.Receivers, 2, "receivers should still emit when no leaves point at them")
	for _, r := range proto.Receivers {
		require.Equal(t, uint64(0), r.AmountSats, "receivers with no leaves should report 0 sats")
	}
}

func TestMarshalProto_PopulatesLeafTransferReceiverID(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)

	proto, err := transfer.MarshalProto(mimoOnContext(t))
	require.NoError(t, err)
	require.Len(t, proto.Leaves, 2)

	receiverIDs := make(map[string]struct{}, 2)
	for _, r := range transfer.Edges.TransferReceivers {
		receiverIDs[r.ID.String()] = struct{}{}
	}
	for _, leaf := range proto.Leaves {
		require.NotEmpty(t, leaf.TransferReceiverId, "each leaf should carry its receiver id")
		_, ok := receiverIDs[leaf.TransferReceiverId]
		require.True(t, ok, "leaf.transfer_receiver_id %s should match one of the transfer's receivers", leaf.TransferReceiverId)
	}
}

func TestMarshalProto_PopulatesLeafTransferSenderID(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)

	proto, err := transfer.MarshalProto(mimoOnContext(t))
	require.NoError(t, err)
	require.Len(t, proto.Leaves, 2)

	// All leaves should carry the same single-sender id from the fixture.
	senderID := transfer.Edges.TransferLeaves[0].TransferSenderID.String()
	for _, leaf := range proto.Leaves {
		require.Equal(t, senderID, leaf.TransferSenderId, "each leaf should carry its sender id")
	}
}

func TestMarshalProto_KnobOff_OmitsMultiParticipantFields(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	senderID := uuid.New()
	transfer.Edges.TransferSenders = []*ent.TransferSender{
		{ID: senderID, IdentityPubkey: transfer.SenderIdentityPubkey},
	}

	// No knob injection -> KnobReadMIMOMultiParticipantFormat defaults to 0.
	proto, err := transfer.MarshalProto(t.Context())
	require.NoError(t, err)

	require.Len(t, proto.Leaves, 2, "Leaves should still emit (legacy field)")
	require.Equal(t, transfer.SenderIdentityPubkey.Serialize(), proto.SenderIdentityPublicKey,
		"legacy scalar SenderIdentityPublicKey should still emit")
	require.Empty(t, proto.Senders, "Senders[] should be empty when knob is off")
	require.Empty(t, proto.Receivers, "Receivers[] should be empty when knob is off")
}

func TestMarshalProtoForReceiver_KnobOff_OmitsMultiParticipantFields(t *testing.T) {
	transfer, recv1Pub, _ := preloadedTransfer(t)
	senderID := uuid.New()
	transfer.Edges.TransferSenders = []*ent.TransferSender{
		{ID: senderID, IdentityPubkey: transfer.SenderIdentityPubkey},
	}

	// MarshalProtoForReceiver still filters leaves by receiver regardless of knob,
	// since the filtering is required for correctness — only the proto Senders[]/Receivers[]
	// fields are gated.
	proto, err := transfer.MarshalProtoForReceiver(t.Context(), recv1Pub)
	require.NoError(t, err)
	require.Len(t, proto.Leaves, 1, "leaf filtering by receiver still applies when knob off")
	require.Empty(t, proto.Senders, "Senders[] should be empty when knob is off")
	require.Empty(t, proto.Receivers, "Receivers[] should be empty when knob is off")
}

func TestMarshalProto_KnobOn_WarnsWhenSendersMissing(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	transfer.Edges.TransferSenders = nil

	ctx, logs := mimoOnContextWithLogObserver(t)
	proto, err := transfer.MarshalProto(ctx)
	require.NoError(t, err)
	require.Empty(t, proto.Senders, "Senders[] is empty when the edge isn't pre-loaded")

	warnings := logs.FilterMessageSnippet("TransferSenders not pre-loaded").All()
	require.Len(t, warnings, 1, "expected one warning about missing TransferSenders edge")
	require.Equal(t, zapcore.WarnLevel, warnings[0].Level)
}

func TestMarshalProto_KnobOn_WarnsWhenReceiversMissing(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	transfer.Edges.TransferReceivers = nil
	transfer.Edges.TransferSenders = []*ent.TransferSender{
		{ID: uuid.New(), IdentityPubkey: transfer.SenderIdentityPubkey},
	}

	ctx, logs := mimoOnContextWithLogObserver(t)
	proto, err := transfer.MarshalProto(ctx)
	require.NoError(t, err)
	require.Empty(t, proto.Receivers, "Receivers[] is empty when the edge isn't pre-loaded")
	require.Len(t, proto.Senders, 1, "Senders[] still emits when its edge is loaded")

	warnings := logs.FilterMessageSnippet("TransferReceivers not pre-loaded").All()
	require.Len(t, warnings, 1, "expected one warning about missing TransferReceivers edge")
}

func TestMarshalProto_KnobOff_NoWarningsWhenEdgesMissing(t *testing.T) {
	transfer, _, _ := preloadedTransfer(t)
	transfer.Edges.TransferReceivers = nil
	transfer.Edges.TransferSenders = nil

	core, logs := observer.New(zapcore.WarnLevel)
	ctx := logging.Inject(t.Context(), zap.New(core))
	// No knob injection -> knob off -> we never inspect the edges -> no warnings.

	_, err := transfer.MarshalProto(ctx)
	require.NoError(t, err)
	require.Zero(t, logs.Len(), "no warnings should fire when the knob is off")
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
