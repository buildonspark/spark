package task

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferreceiver"
)

// fixtureCase describes one (transfer status, receiver status) pair used to
// seed test rows. wantFlipped tells the assertion whether the receiver should
// have been flipped to RECEIVER_CLAIM_PENDING by the backfill.
type fixtureCase struct {
	name           string
	transferStatus st.TransferStatus
	receiverStatus st.TransferReceiverStatus
	wantFlipped    bool
}

func TestBackfillReceiverClaimPending_FlipsOnlyPostTweakInitiated(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cases := []fixtureCase{
		// Post-tweak transfers with INITIATED receivers — these are exactly
		// what the backfill targets.
		{"sender_key_tweaked_initiated", st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusSenderInitiated, true},
		{"receiver_key_tweaked_initiated", st.TransferStatusReceiverKeyTweaked, st.TransferReceiverStatusSenderInitiated, true},
		{"receiver_key_tweak_locked_initiated", st.TransferStatusReceiverKeyTweakLocked, st.TransferReceiverStatusSenderInitiated, true},
		{"receiver_key_tweak_applied_initiated", st.TransferStatusReceiverKeyTweakApplied, st.TransferReceiverStatusSenderInitiated, true},
		{"receiver_refund_signed_initiated", st.TransferStatusReceiverRefundSigned, st.TransferReceiverStatusSenderInitiated, true},

		// Pre-tweak transfers with INITIATED receivers — must NOT flip; the
		// backfill is only meant to fix post-tweak rows that the new write
		// path would have written as RECEIVER_CLAIM_PENDING.
		{"sender_initiated_initiated", st.TransferStatusSenderInitiated, st.TransferReceiverStatusSenderInitiated, false},
		{"sender_initiated_coordinator_initiated", st.TransferStatusSenderInitiatedCoordinator, st.TransferReceiverStatusSenderInitiated, false},
		{"sender_key_tweak_pending_initiated", st.TransferStatusSenderKeyTweakPending, st.TransferReceiverStatusSenderInitiated, false},

		// Already RECEIVER_CLAIM_PENDING — must stay there (idempotency).
		{"sender_key_tweaked_already_pending", st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusReceiverClaimPending, false},

		// Receiver further along — must not be regressed.
		{"sender_key_tweaked_receiver_key_tweaked", st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusKeyTweaked, false},
		{"receiver_refund_signed_completed", st.TransferStatusReceiverRefundSigned, st.TransferReceiverStatusCompleted, false},

		// Terminal sender side — receiver INITIATED but cancelled/expired
		// transfer must not be flipped.
		{"expired_initiated", st.TransferStatusExpired, st.TransferReceiverStatusSenderInitiated, false},
		{"returned_initiated", st.TransferStatusReturned, st.TransferReceiverStatusSenderInitiated, false},
	}

	receiverIDs := seedBackfillFixtures(t, ctx, cases)

	require.NoError(t, backfillReceiverClaimPending(ctx))

	// Re-fetch every receiver and assert its status. Re-fetch the client too,
	// because each ent.DbCommit invalidates the previous tx-bound client.
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for i, c := range cases {
		got, err := client.TransferReceiver.Query().
			Where(transferreceiver.IDEQ(receiverIDs[i])).
			Only(ctx)
		require.NoError(t, err, c.name)

		want := c.receiverStatus
		if c.wantFlipped {
			want = st.TransferReceiverStatusReceiverClaimPending
		}
		assert.Equal(t, want, got.Status, "case %s", c.name)
	}
}

func TestBackfillReceiverClaimPending_Idempotent(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cases := []fixtureCase{
		{"a", st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusSenderInitiated, true},
		{"b", st.TransferStatusReceiverRefundSigned, st.TransferReceiverStatusSenderInitiated, true},
		{"c", st.TransferStatusSenderInitiated, st.TransferReceiverStatusSenderInitiated, false},
	}
	receiverIDs := seedBackfillFixtures(t, ctx, cases)

	// First run does the work.
	require.NoError(t, backfillReceiverClaimPending(ctx))

	// Snapshot update_time after the first run for the rows we expect to be
	// flipped, so we can prove the second run does not re-touch them.
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	firstRunUpdateTimes := make(map[uuid.UUID]time.Time)
	for i, c := range cases {
		if !c.wantFlipped {
			continue
		}
		got, err := client.TransferReceiver.Query().
			Where(transferreceiver.IDEQ(receiverIDs[i])).
			Only(ctx)
		require.NoError(t, err)
		require.Equal(t, st.TransferReceiverStatusReceiverClaimPending, got.Status)
		firstRunUpdateTimes[got.ID] = got.UpdateTime
	}
	// Release the read tx so the second backfill run gets a fresh client.
	require.NoError(t, ent.DbCommit(ctx))

	// Sleep long enough that NOW() in a re-run would produce a strictly later
	// update_time than the snapshot. 50ms is comfortable on Postgres.
	time.Sleep(50 * time.Millisecond)

	// Second run should be a no-op — no rows match the predicate anymore.
	require.NoError(t, backfillReceiverClaimPending(ctx))

	client, err = ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for id, ts := range firstRunUpdateTimes {
		got, err := client.TransferReceiver.Query().
			Where(transferreceiver.IDEQ(id)).
			Only(ctx)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusReceiverClaimPending, got.Status)
		assert.True(t, got.UpdateTime.Equal(ts),
			"second run should not re-touch row %s; first run update_time=%s, second observed=%s",
			id, ts, got.UpdateTime)
	}
}

func TestBackfillReceiverClaimPending_PagesThroughManyRows(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Seed > batchSize candidates to exercise the pagination loop.
	const total = backfillReceiverClaimPendingBatchSize + 137
	makePostTweakInitiated(t, ctx, total)

	require.NoError(t, backfillReceiverClaimPending(ctx))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	stillInitiated, err := client.TransferReceiver.Query().
		Where(transferreceiver.StatusEQ(st.TransferReceiverStatusSenderInitiated)).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, stillInitiated, "backfill should drain all candidates across multiple batches")

	flipped, err := client.TransferReceiver.Query().
		Where(transferreceiver.StatusEQ(st.TransferReceiverStatusReceiverClaimPending)).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, total, flipped)
}

// seedBackfillFixtures inserts one (transfer, receiver) pair per case and
// returns the receiver IDs in case-index order. Commits the seeding tx so the
// rows are visible to subsequent backfill calls (which begin their own txs).
func seedBackfillFixtures(t *testing.T, ctx context.Context, cases []fixtureCase) []uuid.UUID {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	receiverIDs := make([]uuid.UUID, len(cases))
	for i, c := range cases {
		senderPub := keys.GeneratePrivateKey().Public()
		receiverPub := keys.GeneratePrivateKey().Public()

		tr, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(c.transferStatus).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err, c.name)

		rcv, err := client.TransferReceiver.Create().
			SetTransferID(tr.ID).
			SetIdentityPubkey(receiverPub).
			SetStatus(c.receiverStatus).
			Save(ctx)
		require.NoError(t, err, c.name)

		receiverIDs[i] = rcv.ID
	}
	require.NoError(t, ent.DbCommit(ctx))
	return receiverIDs
}

// makePostTweakInitiated creates `n` post-tweak transfers each paired with one
// INITIATED receiver — i.e. exactly the rows the backfill should flip. Commits
// the seeding tx before returning.
func makePostTweakInitiated(t *testing.T, ctx context.Context, n int) {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for range n {
		senderPub := keys.GeneratePrivateKey().Public()
		receiverPub := keys.GeneratePrivateKey().Public()

		tr, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderKeyTweaked).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		_, err = client.TransferReceiver.Create().
			SetTransferID(tr.ID).
			SetIdentityPubkey(receiverPub).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, ent.DbCommit(ctx))
}
