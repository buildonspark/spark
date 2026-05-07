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
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/lightsparkdev/spark/so/ent/transfersender"
)

// noCacheConfig returns a config that skips memcache so tests don't need
// a memcache instance — the task falls back to seeking from the zero UUID
// on every invocation.
func noCacheConfig() *so.Config {
	return &so.Config{Index: 0, CacheURI: ""}
}

func TestBackfillTransferType_PopulatesEdgesFromParent(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// One transfer per type covers the enum surface.
	cases := []st.TransferType{
		st.TransferTypeTransfer,
		st.TransferTypeCounterSwap,
		st.TransferTypeSwap,
		st.TransferTypePreimageSwap,
		st.TransferTypeCooperativeExit,
		st.TransferTypeUtxoSwap,
		st.TransferTypePrimarySwapV3,
		st.TransferTypeCounterSwapV3,
	}
	transferIDs, senderIDs, receiverIDs := seedSP3050Fixtures(t, ctx, cases, false /*alreadyFilled*/)

	require.NoError(t, backfillTransferType(ctx, noCacheConfig(), 100))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for i, want := range cases {
		gotSender, err := client.TransferSender.Query().Where(transfersender.IDEQ(senderIDs[i])).Only(ctx)
		require.NoError(t, err, "sender for transfer %s", transferIDs[i])
		assert.Equal(t, want, gotSender.TransferType, "sender transfer_type for %s", transferIDs[i])

		gotReceiver, err := client.TransferReceiver.Query().Where(transferreceiver.IDEQ(receiverIDs[i])).Only(ctx)
		require.NoError(t, err, "receiver for transfer %s", transferIDs[i])
		assert.Equal(t, want, gotReceiver.TransferType, "receiver transfer_type for %s", transferIDs[i])
	}
}

func TestBackfillTransferType_Idempotent(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cases := []st.TransferType{st.TransferTypeTransfer, st.TransferTypeSwap}
	_, senderIDs, receiverIDs := seedSP3050Fixtures(t, ctx, cases, false)

	// First run populates.
	require.NoError(t, backfillTransferType(ctx, noCacheConfig(), 100))

	// Snapshot update_time on populated rows.
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	senderUpdates := map[uuid.UUID]time.Time{}
	receiverUpdates := map[uuid.UUID]time.Time{}
	for i := range cases {
		s, err := client.TransferSender.Query().Where(transfersender.IDEQ(senderIDs[i])).Only(ctx)
		require.NoError(t, err)
		senderUpdates[s.ID] = s.UpdateTime
		r, err := client.TransferReceiver.Query().Where(transferreceiver.IDEQ(receiverIDs[i])).Only(ctx)
		require.NoError(t, err)
		receiverUpdates[r.ID] = r.UpdateTime
	}
	// Release tx so the next backfill starts fresh.
	require.NoError(t, ent.DbCommit(ctx))

	// Sleep so a re-write would produce a strictly later update_time.
	time.Sleep(50 * time.Millisecond)

	// Second run is a no-op for already-populated rows (transfer_type IS NULL
	// guard in the bulk UPDATE matches nothing).
	require.NoError(t, backfillTransferType(ctx, noCacheConfig(), 100))

	client, err = ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for id, ts := range senderUpdates {
		got, err := client.TransferSender.Query().Where(transfersender.IDEQ(id)).Only(ctx)
		require.NoError(t, err)
		assert.True(t, got.UpdateTime.Equal(ts), "sender %s: second run should not re-touch (first=%s, second=%s)", id, ts, got.UpdateTime)
	}
	for id, ts := range receiverUpdates {
		got, err := client.TransferReceiver.Query().Where(transferreceiver.IDEQ(id)).Only(ctx)
		require.NoError(t, err)
		assert.True(t, got.UpdateTime.Equal(ts), "receiver %s: second run should not re-touch (first=%s, second=%s)", id, ts, got.UpdateTime)
	}
}

func TestBackfillTransferType_DoesNotOverwriteExisting(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// alreadyFilled=true seeds edge rows with transfer_type already set to a
	// value that does NOT match the parent transfer's type — proving the
	// backfill won't clobber a populated cell even if it disagrees with
	// the parent.
	parents := []st.TransferType{st.TransferTypeTransfer}
	_, senderIDs, receiverIDs := seedSP3050Fixtures(t, ctx, parents, true)

	require.NoError(t, backfillTransferType(ctx, noCacheConfig(), 100))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	gotSender, err := client.TransferSender.Query().Where(transfersender.IDEQ(senderIDs[0])).Only(ctx)
	require.NoError(t, err)
	// Pre-seeded value (CounterSwap) must be preserved, not flipped to parent (Transfer).
	assert.Equal(t, st.TransferTypeCounterSwap, gotSender.TransferType)

	gotReceiver, err := client.TransferReceiver.Query().Where(transferreceiver.IDEQ(receiverIDs[0])).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.TransferTypeCounterSwap, gotReceiver.TransferType)
}

func TestBackfillTransferType_HonorsBatchSize(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	const batchSize = 10
	const total = batchSize*2 + 5 // more than one batch's worth of candidates
	parents := make([]st.TransferType, total)
	for i := range parents {
		parents[i] = st.TransferTypeTransfer
	}
	_, senderIDs, receiverIDs := seedSP3050Fixtures(t, ctx, parents, false)

	require.NoError(t, backfillTransferType(ctx, noCacheConfig(), batchSize))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	senderFilled := 0
	for _, id := range senderIDs {
		s, err := client.TransferSender.Query().Where(transfersender.IDEQ(id)).Only(ctx)
		require.NoError(t, err)
		if string(s.TransferType) != "" {
			senderFilled++
		}
	}
	assert.Equal(t, batchSize, senderFilled, "exactly batchSize sender rows should be filled per invocation")

	receiverFilled := 0
	for _, id := range receiverIDs {
		r, err := client.TransferReceiver.Query().Where(transferreceiver.IDEQ(id)).Only(ctx)
		require.NoError(t, err)
		if string(r.TransferType) != "" {
			receiverFilled++
		}
	}
	assert.Equal(t, batchSize, receiverFilled, "exactly batchSize receiver rows should be filled per invocation")
}

func TestBackfillTransferType_MultipleReceivers(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	senderPub := keys.GeneratePrivateKey().Public()
	receiver1 := keys.GeneratePrivateKey().Public()
	receiver2 := keys.GeneratePrivateKey().Public()

	tr, err := client.Transfer.Create().
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(receiver1).
		SetStatus(st.TransferStatusCompleted).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetType(st.TransferTypeCounterSwap).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.TransferSender.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(senderPub).
		Save(ctx)
	require.NoError(t, err)

	rcv1, err := client.TransferReceiver.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(receiver1).
		SetStatus(st.TransferReceiverStatusCompleted).
		Save(ctx)
	require.NoError(t, err)
	rcv2, err := client.TransferReceiver.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(receiver2).
		SetStatus(st.TransferReceiverStatusCompleted).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, ent.DbCommit(ctx))

	require.NoError(t, backfillTransferType(ctx, noCacheConfig(), 100))

	client, err = ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{rcv1.ID, rcv2.ID} {
		got, err := client.TransferReceiver.Query().Where(transferreceiver.IDEQ(id)).Only(ctx)
		require.NoError(t, err)
		assert.Equal(t, st.TransferTypeCounterSwap, got.TransferType, "receiver %s wrong type", id)
	}
}

func TestBackfillTransferType_RespectsCutoff(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Pre-cutoff transfer: should be backfilled.
	preCutoffTypes := []st.TransferType{st.TransferTypeTransfer}
	_, preSenderIDs, preReceiverIDs := seedSP3050FixturesAt(t, ctx, preCutoffTypes, false, sp3050BackfillCutoff.Add(-1*time.Hour))

	// Post-cutoff transfer: dual-write owns these; backfill must not touch.
	postCutoffTypes := []st.TransferType{st.TransferTypeCounterSwap}
	_, postSenderIDs, postReceiverIDs := seedSP3050FixturesAt(t, ctx, postCutoffTypes, false, sp3050BackfillCutoff.Add(1*time.Hour))

	require.NoError(t, backfillTransferType(ctx, noCacheConfig(), 100))

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Pre-cutoff edge rows: populated.
	preSender, err := client.TransferSender.Query().Where(transfersender.IDEQ(preSenderIDs[0])).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.TransferTypeTransfer, preSender.TransferType, "pre-cutoff sender should be backfilled")
	preReceiver, err := client.TransferReceiver.Query().Where(transferreceiver.IDEQ(preReceiverIDs[0])).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.TransferTypeTransfer, preReceiver.TransferType, "pre-cutoff receiver should be backfilled")

	// Post-cutoff edge rows: still NULL (zero value of TransferType is "").
	postSender, err := client.TransferSender.Query().Where(transfersender.IDEQ(postSenderIDs[0])).Only(ctx)
	require.NoError(t, err)
	assert.Empty(t, string(postSender.TransferType), "post-cutoff sender must NOT be backfilled (dual-write owns it)")
	postReceiver, err := client.TransferReceiver.Query().Where(transferreceiver.IDEQ(postReceiverIDs[0])).Only(ctx)
	require.NoError(t, err)
	assert.Empty(t, string(postReceiver.TransferType), "post-cutoff receiver must NOT be backfilled (dual-write owns it)")
}

func TestSP3050BackfillCursorKey_IncludesOperatorIndex(t *testing.T) {
	t.Parallel()
	k0 := sp3050BackfillCursorKey(0)
	k1 := sp3050BackfillCursorKey(1)
	assert.Contains(t, k0, ":0")
	assert.Contains(t, k1, ":1")
	assert.NotEqual(t, k0, k1)
}

func TestValidateSP3050Table_Whitelists(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateSP3050Table("transfer_senders"))
	require.NoError(t, validateSP3050Table("transfer_receivers"))
	require.Error(t, validateSP3050Table("transfers"))
	require.Error(t, validateSP3050Table("'; DROP TABLE foo; --"))
}

// preCutoffCreateTime is a fixed timestamp safely before sp3050BackfillCutoff
// so test fixtures are visited by the backfill regardless of when the test
// runs. Anchored a year before the cutoff to leave plenty of room.
var preCutoffCreateTime = sp3050BackfillCutoff.Add(-365 * 24 * time.Hour)

// seedSP3050Fixtures creates one (transfer, sender, receiver) triple per
// supplied transfer type. All rows are created with create_time set to
// preCutoffCreateTime so the backfill's cutoff predicate matches them.
// When alreadyFilled is true, the edge rows are created with a non-matching
// transfer_type pre-set so the test can verify the backfill won't overwrite
// an existing value.
//
// Returns parallel slices: transferIDs, senderIDs, receiverIDs.
func seedSP3050Fixtures(t *testing.T, ctx context.Context, parents []st.TransferType, alreadyFilled bool) ([]uuid.UUID, []uuid.UUID, []uuid.UUID) {
	t.Helper()
	return seedSP3050FixturesAt(t, ctx, parents, alreadyFilled, preCutoffCreateTime)
}

// seedSP3050FixturesAt is like seedSP3050Fixtures but lets the caller pin
// every row's create_time to a specific instant. Used by the cutoff test
// to seed rows on either side of sp3050BackfillCutoff.
func seedSP3050FixturesAt(t *testing.T, ctx context.Context, parents []st.TransferType, alreadyFilled bool, createTime time.Time) ([]uuid.UUID, []uuid.UUID, []uuid.UUID) {
	t.Helper()
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	transferIDs := make([]uuid.UUID, len(parents))
	senderIDs := make([]uuid.UUID, len(parents))
	receiverIDs := make([]uuid.UUID, len(parents))

	for i, parentType := range parents {
		senderPub := keys.GeneratePrivateKey().Public()
		receiverPub := keys.GeneratePrivateKey().Public()

		tr, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusCompleted).
			SetTotalValue(1000).
			SetExpiryTime(createTime.Add(10 * time.Minute)).
			SetType(parentType).
			SetNetwork(btcnetwork.Regtest).
			SetCreateTime(createTime).
			Save(ctx)
		require.NoError(t, err)

		senderCreate := client.TransferSender.Create().
			SetTransferID(tr.ID).
			SetIdentityPubkey(senderPub).
			SetCreateTime(createTime)
		if alreadyFilled {
			senderCreate = senderCreate.SetTransferType(st.TransferTypeCounterSwap)
		}
		s, err := senderCreate.Save(ctx)
		require.NoError(t, err)

		receiverCreate := client.TransferReceiver.Create().
			SetTransferID(tr.ID).
			SetIdentityPubkey(receiverPub).
			SetStatus(st.TransferReceiverStatusCompleted).
			SetCreateTime(createTime)
		if alreadyFilled {
			receiverCreate = receiverCreate.SetTransferType(st.TransferTypeCounterSwap)
		}
		r, err := receiverCreate.Save(ctx)
		require.NoError(t, err)

		transferIDs[i] = tr.ID
		senderIDs[i] = s.ID
		receiverIDs[i] = r.ID
	}
	require.NoError(t, ent.DbCommit(ctx))
	return transferIDs, senderIDs, receiverIDs
}
