//go:build lightspark

package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for GetStuckTransfers. These drive the public RPC handler (not the
// internal queryStuckTransferIDs helper) so the tests survive refactors
// of the query shape as long as the handler contract holds.
//
// All tests use Postgres because the handler's raw SQL relies on the partial
// indexes + pq.Array bindings, neither of which SQLite supports.

// stuckFixture is minimal test scaffolding — sets up ctx, a Postgres-backed
// Ent client, and the handler under test.
type stuckFixture struct {
	t       *testing.T
	ctx     context.Context
	client  *ent.Client
	handler *SspRequestHandler
	rng     *rand.ChaCha8
	baseNow time.Time // reference "now" for placing rows on the timeline
}

func newStuckFixture(t *testing.T) *stuckFixture {
	t.Helper()
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	return &stuckFixture{
		t:       t,
		ctx:     ctx,
		client:  client,
		handler: NewSspRequestHandler(&so.Config{Identifier: "test-operator"}),
		rng:     rand.NewChaCha8([32]byte{}),
		baseNow: time.Now(),
	}
}

func (f *stuckFixture) newPubkey() keys.Public {
	return keys.MustGeneratePrivateKeyFromRand(f.rng).Public()
}

// transferOpts describes a fixture transfer. `transferState`, `sender`, and
// `receiver` are required. `receiverState` defaults to INITIATED.
// `createTime` defaults to 2h before the fixture's baseNow (safely inside
// the 1-hour `before` cutoff that GetStuckTransfers enforces). The primary
// receiver row inherits `createTime` per the cross-participant create_time
// invariant.
type transferOpts struct {
	network        btcnetwork.Network
	transferState  st.TransferStatus
	sender         keys.Public
	receiver       keys.Public
	receiverState  st.TransferReceiverStatus
	extraReceivers []extraReceiver
	createTime     time.Time
}

type extraReceiver struct {
	pubkey keys.Public
	status st.TransferReceiverStatus
}

// makeTransfer creates a Transfer plus one TransferSender and one or more
// TransferReceivers. This is fixture setup, not a public API — but there is
// no public "create transfer" entry point on SspRequestHandler that we can
// reach without wiring through the full transfer protocol. Ent builders are
// the minimum scaffolding; assertions still go through the handler RPC.
func (f *stuckFixture) makeTransfer(opts transferOpts) *ent.Transfer {
	f.t.Helper()
	network := opts.network
	if network == btcnetwork.Unspecified {
		network = btcnetwork.Regtest
	}
	createTime := opts.createTime
	if createTime.IsZero() {
		createTime = f.baseNow.Add(-2 * time.Hour)
	}
	receiverState := opts.receiverState
	if receiverState == "" {
		receiverState = st.TransferReceiverStatusInitiated
	}

	transfer, err := f.client.Transfer.Create().
		SetNetwork(network).
		SetType(st.TransferTypeTransfer).
		SetStatus(opts.transferState).
		SetExpiryTime(f.baseNow.Add(24 * time.Hour)).
		SetTotalValue(1000).
		SetSenderIdentityPubkey(opts.sender).
		SetReceiverIdentityPubkey(opts.receiver).
		SetCreateTime(createTime).
		Save(f.ctx)
	require.NoError(f.t, err)

	_, err = f.client.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(opts.sender).
		SetCreateTime(createTime).
		SetTransferType(transfer.Type).
		Save(f.ctx)
	require.NoError(f.t, err)

	_, err = f.client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(opts.receiver).
		SetStatus(receiverState).
		SetCreateTime(createTime).
		SetTransferType(transfer.Type).
		Save(f.ctx)
	require.NoError(f.t, err)

	for _, extra := range opts.extraReceivers {
		_, err := f.client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(extra.pubkey).
			SetStatus(extra.status).
			SetCreateTime(createTime).
			SetTransferType(transfer.Type).
			Save(f.ctx)
		require.NoError(f.t, err)
	}
	return transfer
}

// getStuckTransferIDs invokes the handler and returns just the transfer IDs
// for concise assertions. `network` defaults to REGTEST if UNSPECIFIED, since
// all test fixtures default to Regtest.
func (f *stuckFixture) getStuckTransferIDs(user keys.Public, network pb.Network, limit, offset int64) []uuid.UUID {
	f.t.Helper()
	req := &pbssp.GetStuckTransfersRequest{
		UserIdentityPublicKey: user.Serialize(),
		Network:               network,
		Limit:                 limit,
		Offset:                offset,
	}
	resp, err := f.handler.GetStuckTransfers(f.ctx, req)
	require.NoError(f.t, err)
	ids := make([]uuid.UUID, 0, len(resp.GetTransfers()))
	for _, st := range resp.GetTransfers() {
		id, err := uuid.Parse(st.GetTransfer().GetId())
		require.NoError(f.t, err)
		ids = append(ids, id)
	}
	return ids
}

// -----------------------------------------------------------------------------
// Core correctness
// -----------------------------------------------------------------------------

func TestGetStuckTransfers_MIMO_Empty(t *testing.T) {
	f := newStuckFixture(t)
	unrelated := f.newPubkey()

	ids := f.getStuckTransferIDs(unrelated, pb.Network_UNSPECIFIED, 50, 0)
	assert.Empty(t, ids)
}

func TestGetStuckTransfers_MIMO_ReceiverOnly(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	t1 := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
	})
	t2 := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverRefundSigned,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusRefundSigned,
	})
	// Completed — must not appear.
	_ = f.makeTransfer(transferOpts{
		transferState: st.TransferStatusCompleted,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusCompleted,
	})

	ids := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0)
	assert.ElementsMatch(t, []uuid.UUID{t1.ID, t2.ID}, ids)
}

func TestGetStuckTransfers_MIMO_SenderOnly(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	t1 := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderInitiated,
		sender:        user,
		receiver:      f.newPubkey(),
	})
	t2 := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderKeyTweakPending,
		sender:        user,
		receiver:      f.newPubkey(),
	})
	// Past sender-stuck — must not appear.
	_ = f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderKeyTweaked,
		sender:        user,
		receiver:      f.newPubkey(),
	})

	ids := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0)
	assert.ElementsMatch(t, []uuid.UUID{t1.ID, t2.ID}, ids)
}

func TestGetStuckTransfers_MIMO_BothSenderAndReceiver(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	senderStuck := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderInitiated,
		sender:        user,
		receiver:      f.newPubkey(),
	})
	receiverStuck := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
	})

	ids := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0)
	assert.ElementsMatch(t, []uuid.UUID{senderStuck.ID, receiverStuck.ID}, ids)
}

// TestGetStuckTransfers_MIMO_SelfTransfer_ReceiverStuck is the regression
// test for the NOT EXISTS anti-join bug flagged in PR #6280 review. A self-
// transfer (user is both sender + receiver) whose `transfers.status` has
// advanced past the sender-stuck set should still be returned when the
// user's own receiver row is stuck.
func TestGetStuckTransfers_MIMO_SelfTransfer_ReceiverStuck(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	self := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverRefundSigned,
		sender:        user, // user is also a sender
		receiver:      user,
		receiverState: st.TransferReceiverStatusRefundSigned,
	})

	ids := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0)
	assert.ElementsMatch(t, []uuid.UUID{self.ID}, ids,
		"self-transfer stuck on the receiver side must be returned; the NOT EXISTS anti-join used to swallow it")
}

// TestGetStuckTransfers_MIMO_InitiatedNotStuck codifies the intent that
// INITIATED receivers are waiting (not stuck) — the receiver partial's
// WHERE clause excludes INITIATED for that reason.
func TestGetStuckTransfers_MIMO_InitiatedNotStuck(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	_ = f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusInitiated, // == "INITIATED"
	})

	ids := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0)
	assert.Empty(t, ids, "INITIATED receivers are waiting, not stuck")
}

// TestGetStuckTransfers_MIMO_ReceiverClaimPendingNotStuck codifies the
// deliberate exclusion of RECEIVER_CLAIM_PENDING from mimo.StuckReceiverStatuses.
// Sender has finished its key-tweak handoff (transfer at SENDER_KEY_TWEAKED)
// and the receiver is in the post-tweak/pre-claim window; they haven't started
// claiming yet, so they aren't stuck — they just haven't polled.
func TestGetStuckTransfers_MIMO_ReceiverClaimPendingNotStuck(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	_ = f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusReceiverClaimPending,
	})

	ids := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0)
	assert.Empty(t, ids, "RECEIVER_CLAIM_PENDING receivers haven't started claiming, not stuck")
}

// TestGetStuckTransfers_MIMO_MultiReceiver_PerReceiverSemantic codifies the
// MIMO V1 design intent from the PR #6280 review: stuck is per-receiver.
// Two users sharing a multi-receiver transfer see different results
// depending on their own receiver row's state.
func TestGetStuckTransfers_MIMO_MultiReceiver_PerReceiverSemantic(t *testing.T) {
	f := newStuckFixture(t)
	completed := f.newPubkey()
	stuck := f.newPubkey()

	t1 := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      completed,
		receiverState: st.TransferReceiverStatusCompleted,
		extraReceivers: []extraReceiver{
			{pubkey: stuck, status: st.TransferReceiverStatusKeyTweaked},
		},
	})

	completedIDs := f.getStuckTransferIDs(completed, pb.Network_UNSPECIFIED, 50, 0)
	assert.Empty(t, completedIDs, "a receiver already completed should not see the transfer as stuck, even when other receivers are")

	stuckIDs := f.getStuckTransferIDs(stuck, pb.Network_UNSPECIFIED, 50, 0)
	assert.ElementsMatch(t, []uuid.UUID{t1.ID}, stuckIDs)
}

// -----------------------------------------------------------------------------
// Filters
// -----------------------------------------------------------------------------

// makeTransferWithLeaf creates a fixture transfer AND its accompanying Tree
// + SigningKeyshare + TreeNode + TransferLeaf, which the handler's network
// filter requires via HasTransferLeavesWith(HasLeafWith(NetworkEQ)). Used
// only by network-filter tests; most tests use makeTransfer + UNSPECIFIED.
func (f *stuckFixture) makeTransferWithLeaf(opts transferOpts) *ent.Transfer {
	f.t.Helper()
	network := opts.network
	if network == btcnetwork.Unspecified {
		network = btcnetwork.Regtest
	}

	baseTxid := st.NewRandomTxIDForTesting(f.t)
	tree, err := f.client.Tree.Create().
		SetNetwork(network).
		SetOwnerIdentityPubkey(opts.sender).
		SetBaseTxid(baseTxid).
		SetVout(0).
		SetStatus(st.TreeStatusAvailable).
		Save(f.ctx)
	require.NoError(f.t, err)

	secret := keys.MustGeneratePrivateKeyFromRand(f.rng)
	keyshare, err := f.client.SigningKeyshare.Create().
		SetPublicShares(map[string]keys.Public{"key": secret.Public()}).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicKey(f.newPubkey()).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(f.ctx)
	require.NoError(f.t, err)

	leaf, err := f.client.TreeNode.Create().
		SetTree(tree).
		SetNetwork(network).
		SetValue(1000).
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetVerifyingPubkey(f.newPubkey()).
		SetOwnerIdentityPubkey(opts.sender).
		SetOwnerSigningPubkey(f.newPubkey()).
		SetRawTx(createTestTxBytesWithIndex(f.t, 1000, 0)).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(f.ctx)
	require.NoError(f.t, err)

	// Build the transfer + sender/receiver rows via makeTransfer.
	opts.network = network
	transfer := f.makeTransfer(opts)

	_, err = f.client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(f.t, 1001)).
		SetIntermediateRefundTx(createTestTxBytes(f.t, 1002)).
		Save(f.ctx)
	require.NoError(f.t, err)

	return transfer
}

// TestGetStuckTransfers_MIMO_NetworkFilter exercises the network filter
// end-to-end. Requires transfer_leaves fixtures because the handler's filter
// uses HasTransferLeavesWith(HasLeafWith(NetworkEQ(...))).
func TestGetStuckTransfers_MIMO_NetworkFilter(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	regtest := f.makeTransferWithLeaf(transferOpts{
		network:       btcnetwork.Regtest,
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
	})
	mainnet := f.makeTransferWithLeaf(transferOpts{
		network:       btcnetwork.Mainnet,
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
	})

	assert.ElementsMatch(t, []uuid.UUID{regtest.ID},
		f.getStuckTransferIDs(user, pb.Network_REGTEST, 50, 0))
	assert.ElementsMatch(t, []uuid.UUID{mainnet.ID},
		f.getStuckTransferIDs(user, pb.Network_MAINNET, 50, 0))
	assert.ElementsMatch(t, []uuid.UUID{regtest.ID, mainnet.ID},
		f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0),
		"UNSPECIFIED returns all networks")
}

// TestGetStuckTransfers_MIMO_BeforeCutoff verifies the `before` parameter is
// respected end-to-end. Drives via req.Before on the RPC, not by calling the
// helper with a different cutoff.
func TestGetStuckTransfers_MIMO_BeforeCutoff(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	old := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-3 * time.Hour),
	})
	recent := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-90 * time.Minute), // outside default 1h cutoff, inside 2h cutoff
	})

	// Default cutoff (now - 1h) picks up both.
	resp, err := f.handler.GetStuckTransfers(f.ctx, &pbssp.GetStuckTransfersRequest{
		UserIdentityPublicKey: user.Serialize(),
		Network:               pb.Network_UNSPECIFIED,
		Limit:                 50,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTransfers(), 2)

	// Tighter cutoff (now - 2h) excludes the 90-minute-old row, leaving only the 3h-old.
	resp, err = f.handler.GetStuckTransfers(f.ctx, &pbssp.GetStuckTransfersRequest{
		UserIdentityPublicKey: user.Serialize(),
		Network:               pb.Network_UNSPECIFIED,
		Before:                timestamppb.New(f.baseNow.Add(-2 * time.Hour)),
		Limit:                 50,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTransfers(), 1)
	assert.Equal(t, old.ID.String(), resp.GetTransfers()[0].GetTransfer().GetId())
	assert.NotEqual(t, recent.ID.String(), resp.GetTransfers()[0].GetTransfer().GetId())
}

// -----------------------------------------------------------------------------
// Ordering + pagination
// -----------------------------------------------------------------------------

func TestGetStuckTransfers_MIMO_OrderedByCreateTimeDesc(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	newest := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-70 * time.Minute),
	})
	middle := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-2 * time.Hour),
	})
	oldest := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-3 * time.Hour),
	})

	ids := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0)
	assert.Equal(t, []uuid.UUID{newest.ID, middle.ID, oldest.ID}, ids)
}

// TestGetStuckTransfers_MIMO_Pagination exercises the per-arm
// `LIMIT = offset + limit` contract end-to-end. With distinct create_times,
// page1 + page2 must equal the combined top-N, in order and without overlap.
// This is the regression test for the pagination bug we hit during
// validation — per-arm `LIMIT = limit` (the bug) would have let page2 pick up
// rows that don't belong on page2.
func TestGetStuckTransfers_MIMO_Pagination(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	// 10 stuck transfers, one per minute going back from -70m.
	for i := range 10 {
		f.makeTransfer(transferOpts{
			transferState: st.TransferStatusReceiverKeyTweaked,
			sender:        f.newPubkey(),
			receiver:      user,
			receiverState: st.TransferReceiverStatusKeyTweaked,
			createTime:    f.baseNow.Add(-time.Duration(70+i) * time.Minute),
		})
	}

	page1 := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 5, 0)
	page2 := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 5, 5)
	combined := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 10, 0)

	require.Len(t, page1, 5)
	require.Len(t, page2, 5)
	require.Len(t, combined, 10)
	assert.Equal(t, combined[:5], page1, "page1 matches first half of combined")
	assert.Equal(t, combined[5:], page2, "page2 matches second half of combined")

	seen := make(map[uuid.UUID]struct{}, 10)
	for _, id := range page1 {
		seen[id] = struct{}{}
	}
	for _, id := range page2 {
		_, dup := seen[id]
		assert.False(t, dup, "page2 must not overlap page1: id=%s", id)
	}
}

// TestGetStuckTransfers_MIMO_TieBreaker exercises the `(create_time DESC,
// id DESC)` tiebreaker. With tied create_times, ordering must be
// deterministic — higher transfer UUID first.
func TestGetStuckTransfers_MIMO_TieBreaker(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()
	tiedTime := f.baseNow.Add(-2 * time.Hour)

	tA := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    tiedTime,
	})
	tB := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    tiedTime,
	})

	ids := f.getStuckTransferIDs(user, pb.Network_UNSPECIFIED, 50, 0)
	require.Len(t, ids, 2)

	first, second := tA.ID, tB.ID
	if tA.ID.String() < tB.ID.String() {
		first, second = tB.ID, tA.ID
	}
	assert.Equal(t, []uuid.UUID{first, second}, ids,
		"transfer_id DESC tiebreaker must return the higher UUID first")
}

// -----------------------------------------------------------------------------
// No-pubkey (operator-wide) MIMO path
// -----------------------------------------------------------------------------

// getAllStuckTransferIDs invokes the handler without a user pubkey, matching
// the operator-facing "find every stuck transfer" query.
func (f *stuckFixture) getAllStuckTransferIDs(network pb.Network, limit, offset int64) []uuid.UUID {
	f.t.Helper()
	req := &pbssp.GetStuckTransfersRequest{
		Network: network,
		Limit:   limit,
		Offset:  offset,
	}
	resp, err := f.handler.GetStuckTransfers(f.ctx, req)
	require.NoError(f.t, err)
	ids := make([]uuid.UUID, 0, len(resp.GetTransfers()))
	for _, stp := range resp.GetTransfers() {
		id, err := uuid.Parse(stp.GetTransfer().GetId())
		require.NoError(f.t, err)
		ids = append(ids, id)
	}
	return ids
}

func TestGetStuckTransfers_MIMO_NoPubkey_Empty(t *testing.T) {
	f := newStuckFixture(t)
	ids := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	assert.Empty(t, ids)
}

// TestGetStuckTransfers_MIMO_NoPubkey_AllUsers confirms the no-pubkey path
// sweeps across multiple users: one sender-stuck, one receiver-stuck, and a
// completed transfer that must be excluded.
func TestGetStuckTransfers_MIMO_NoPubkey_AllUsers(t *testing.T) {
	f := newStuckFixture(t)
	userA := f.newPubkey()
	userB := f.newPubkey()

	senderStuck := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderInitiated,
		sender:        userA,
		receiver:      f.newPubkey(),
	})
	receiverStuck := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      userB,
		receiverState: st.TransferReceiverStatusKeyTweaked,
	})
	// Completed — must not appear.
	_ = f.makeTransfer(transferOpts{
		transferState: st.TransferStatusCompleted,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusCompleted,
	})

	ids := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	assert.ElementsMatch(t, []uuid.UUID{senderStuck.ID, receiverStuck.ID}, ids)
}

// TestGetStuckTransfers_MIMO_NoPubkey_MultiReceiver_Dedup is the regression
// test for the no-pubkey receiver arm: a multi-receiver transfer with two
// stuck receivers must return the transfer ID exactly once. Without SELECT
// DISTINCT, the INNER JOIN would emit one row per stuck receiver.
func TestGetStuckTransfers_MIMO_NoPubkey_MultiReceiver_Dedup(t *testing.T) {
	f := newStuckFixture(t)

	t1 := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		extraReceivers: []extraReceiver{
			{pubkey: f.newPubkey(), status: st.TransferReceiverStatusKeyTweakLocked},
			{pubkey: f.newPubkey(), status: st.TransferReceiverStatusRefundSigned},
		},
	})

	ids := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	assert.Equal(t, []uuid.UUID{t1.ID}, ids,
		"multi-receiver transfer with multiple stuck receivers must appear exactly once")
}

func TestGetStuckTransfers_MIMO_NoPubkey_NetworkFilter(t *testing.T) {
	f := newStuckFixture(t)

	regtest := f.makeTransfer(transferOpts{
		network:       btcnetwork.Regtest,
		transferState: st.TransferStatusSenderInitiated,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
	})
	_ = f.makeTransfer(transferOpts{
		network:       btcnetwork.Mainnet,
		transferState: st.TransferStatusSenderInitiated,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
	})

	regtestIDs := f.getAllStuckTransferIDs(pb.Network_REGTEST, 50, 0)
	assert.Equal(t, []uuid.UUID{regtest.ID}, regtestIDs)

	allIDs := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	assert.Len(t, allIDs, 2)
}

func TestGetStuckTransfers_MIMO_NoPubkey_Pagination(t *testing.T) {
	f := newStuckFixture(t)

	// Three stuck transfers, timestamps descending by ID.
	newest := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderInitiated,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		createTime:    f.baseNow.Add(-2 * time.Hour),
	})
	middle := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-3 * time.Hour),
	})
	oldest := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderKeyTweakPending,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		createTime:    f.baseNow.Add(-4 * time.Hour),
	})

	page1 := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 2, 0)
	page2 := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 2, 2)
	assert.Equal(t, []uuid.UUID{newest.ID, middle.ID}, page1)
	assert.Equal(t, []uuid.UUID{oldest.ID}, page2)
}

// TestGetStuckTransfers_MIMO_NoPubkey_InitiatedNotStuck verifies that an
// INITIATED receiver row never surfaces on the no-pubkey path. The new
// idx_transferreceiver_stuck_create_time partial doesn't include INITIATED
// in its WHERE clause, so this is structurally a tighter guarantee than the
// with-pubkey case (whose partial covers INITIATED + 4 stuck and relies on
// the query's status filter to exclude INITIATED).
func TestGetStuckTransfers_MIMO_NoPubkey_InitiatedNotStuck(t *testing.T) {
	f := newStuckFixture(t)

	_ = f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusInitiated, // == "INITIATED"
	})

	ids := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	assert.Empty(t, ids, "INITIATED receivers must not appear in no-pubkey results")
}

// TestGetStuckTransfers_MIMO_NoPubkey_ReceiverClaimPendingNotStuck verifies
// that a RECEIVER_CLAIM_PENDING receiver row never surfaces on the no-pubkey
// path either. Same rationale as the INITIATED variant — the receiver hasn't
// started claiming yet.
func TestGetStuckTransfers_MIMO_NoPubkey_ReceiverClaimPendingNotStuck(t *testing.T) {
	f := newStuckFixture(t)

	_ = f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusReceiverClaimPending,
	})

	ids := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	assert.Empty(t, ids, "RECEIVER_CLAIM_PENDING receivers must not appear in no-pubkey results")
}

// TestGetStuckTransfers_MIMO_NoPubkey_BeforeCutoff exercises the
// `r.create_time < cutoff` predicate on the no-pubkey path. The handler's
// 1-hour default and a tighter explicit override must both be honored.
func TestGetStuckTransfers_MIMO_NoPubkey_BeforeCutoff(t *testing.T) {
	f := newStuckFixture(t)

	old := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-3 * time.Hour),
	})
	recent := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-90 * time.Minute), // outside default 1h cutoff, inside 2h cutoff
	})

	// Default cutoff (now - 1h) picks up both.
	resp, err := f.handler.GetStuckTransfers(f.ctx, &pbssp.GetStuckTransfersRequest{
		Network: pb.Network_UNSPECIFIED,
		Limit:   50,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTransfers(), 2)

	// Tighter cutoff (now - 2h) excludes the 90-minute-old row, leaving only the 3h-old.
	resp, err = f.handler.GetStuckTransfers(f.ctx, &pbssp.GetStuckTransfersRequest{
		Network: pb.Network_UNSPECIFIED,
		Before:  timestamppb.New(f.baseNow.Add(-2 * time.Hour)),
		Limit:   50,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTransfers(), 1)
	assert.Equal(t, old.ID.String(), resp.GetTransfers()[0].GetTransfer().GetId())
	assert.NotEqual(t, recent.ID.String(), resp.GetTransfers()[0].GetTransfer().GetId())
}

// TestGetStuckTransfers_MIMO_NoPubkey_OrderedByCreateTimeDesc verifies the
// new partial drives ordered top-N natively (no Sort node above the index
// scan). Ordering is by r.create_time, equivalent to t.create_time under the
// cross-participant invariant.
func TestGetStuckTransfers_MIMO_NoPubkey_OrderedByCreateTimeDesc(t *testing.T) {
	f := newStuckFixture(t)

	newest := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-70 * time.Minute),
	})
	middle := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-2 * time.Hour),
	})
	oldest := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-3 * time.Hour),
	})

	ids := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	assert.Equal(t, []uuid.UUID{newest.ID, middle.ID, oldest.ID}, ids)
}

// TestGetStuckTransfers_MIMO_NoPubkey_TieBreaker verifies the
// (create_time DESC, transfer_id DESC) tiebreaker on the new partial.
// Without the transfer_id DESC component, pagination would not be
// deterministic across rows with equal create_time.
func TestGetStuckTransfers_MIMO_NoPubkey_TieBreaker(t *testing.T) {
	f := newStuckFixture(t)
	tiedTime := f.baseNow.Add(-2 * time.Hour)

	tA := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    tiedTime,
	})
	tB := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    tiedTime,
	})

	ids := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	require.Len(t, ids, 2)

	first, second := tA.ID, tB.ID
	if tA.ID.String() < tB.ID.String() {
		first, second = tB.ID, tA.ID
	}
	assert.Equal(t, []uuid.UUID{first, second}, ids,
		"transfer_id DESC tiebreaker must return the higher UUID first")
}

// TestGetStuckTransfers_MIMO_NoPubkey_BothArmsOrdering verifies that the
// UNION ALL across sender and receiver arms produces a globally-ordered
// stream by create_time, not arm-then-arm concatenation. The planner
// achieves this via Merge Append over two pre-sorted children — if either
// arm's ordering or the union-level alias broke, this test would catch it.
func TestGetStuckTransfers_MIMO_NoPubkey_BothArmsOrdering(t *testing.T) {
	f := newStuckFixture(t)

	// Alternating arms by create_time: sender (newest), receiver (middle), sender (oldest).
	newestSender := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderInitiated,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		createTime:    f.baseNow.Add(-70 * time.Minute),
	})
	middleReceiver := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		receiverState: st.TransferReceiverStatusKeyTweaked,
		createTime:    f.baseNow.Add(-2 * time.Hour),
	})
	oldestSender := f.makeTransfer(transferOpts{
		transferState: st.TransferStatusSenderKeyTweakPending,
		sender:        f.newPubkey(),
		receiver:      f.newPubkey(),
		createTime:    f.baseNow.Add(-3 * time.Hour),
	})

	ids := f.getAllStuckTransferIDs(pb.Network_UNSPECIFIED, 50, 0)
	assert.Equal(t, []uuid.UUID{newestSender.ID, middleReceiver.ID, oldestSender.ID}, ids,
		"union of sender and receiver arms must interleave by create_time, not concatenate by arm")
}

// -----------------------------------------------------------------------------
// SigningKeysharePublicShares — exercises marshalStuckTransfer's edge walk over
// preloaded transfer_leaves -> leaf -> signing_keyshare. Regression guard: if a
// query path forgets the preload chain, the leaf edge will be nil and the
// handler will return an error rather than silently emitting an empty map.
// -----------------------------------------------------------------------------

func assertStuckTransferKeyshare(t *testing.T, f *stuckFixture, stuck *pbssp.StuckTransfer, transfer *ent.Transfer) {
	t.Helper()
	leaves, err := transfer.QueryTransferLeaves().QueryLeaf().All(f.ctx)
	require.NoError(t, err)
	require.Len(t, leaves, 1)
	leafID := leaves[0].ID.String()

	require.Contains(t, stuck.GetSigningKeysharePublicShares(), leafID,
		"expected SigningKeysharePublicShares to include entry for attached leaf")
	assert.NotEmpty(t, stuck.GetSigningKeysharePublicShares()[leafID].GetPublicShares(),
		"expected non-empty PublicShares for leaf")
}

func TestGetStuckTransfers_MIMO_IncludesSigningKeyshares(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	transfer := f.makeTransferWithLeaf(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
	})

	resp, err := f.handler.GetStuckTransfers(f.ctx, &pbssp.GetStuckTransfersRequest{
		UserIdentityPublicKey: user.Serialize(),
		Limit:                 50,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTransfers(), 1)
	require.Equal(t, transfer.ID.String(), resp.GetTransfers()[0].GetTransfer().GetId())
	assertStuckTransferKeyshare(t, f, resp.GetTransfers()[0], transfer)
}

func TestQueryStuckTransfer_IncludesSigningKeyshares(t *testing.T) {
	f := newStuckFixture(t)
	user := f.newPubkey()

	transfer := f.makeTransferWithLeaf(transferOpts{
		transferState: st.TransferStatusReceiverKeyTweaked,
		sender:        f.newPubkey(),
		receiver:      user,
		receiverState: st.TransferReceiverStatusKeyTweaked,
	})

	resp, err := f.handler.QueryStuckTransfer(f.ctx, &pbssp.QueryStuckTransferRequest{
		Id: transfer.ID.String(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetTransfer())
	require.Equal(t, transfer.ID.String(), resp.GetTransfer().GetTransfer().GetId())
	assertStuckTransferKeyshare(t, f, resp.GetTransfer(), transfer)
}
