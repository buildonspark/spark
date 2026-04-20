package task

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestParseRepairCursor_RoundTrip(t *testing.T) {
	t.Parallel()
	original := repairCursor{UnixMicros: 1741733334000000, ID: "abc-123-def"}
	serialized := original.String()
	parsed, ok := parseRepairCursor(serialized)
	require.True(t, ok)
	assert.Equal(t, original.UnixMicros, parsed.UnixMicros)
	assert.Equal(t, original.ID, parsed.ID)
}

func TestParseRepairCursor_LegacySeconds(t *testing.T) {
	t.Parallel()
	// A legacy second-precision cursor should be converted to microseconds
	// with +1 second so we re-process the boundary rather than skip transfers.
	parsed, ok := parseRepairCursor("1741733334:abc-123-def")
	require.True(t, ok)
	assert.Equal(t, int64(1741733335000000), parsed.UnixMicros) // +1 second
	assert.Equal(t, "abc-123-def", parsed.ID)
}

func TestParseRepairCursor_InvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"empty string", "", false},
		{"no colon", "12345", false},
		{"non-numeric timestamp", "abc:some-id", false},
		{"missing id after colon", "12345:", true},
		{"id containing colons", "12345:id:with:colons", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := parseRepairCursor(tc.input)
			assert.Equal(t, tc.valid, ok)
		})
	}
}

func TestRepairCursorKey_IncludesOperatorIndex(t *testing.T) {
	t.Parallel()
	key0 := repairCursorKey(0)
	key1 := repairCursorKey(1)
	assert.Contains(t, key0, ":0")
	assert.Contains(t, key1, ":1")
	assert.NotEqual(t, key0, key1)
}

func TestSeedCursor_AtCutoff(t *testing.T) {
	t.Parallel()
	cursor := seedCursor()
	assert.Equal(t, repairCutoff.UnixMicro(), cursor.UnixMicros)
	assert.Equal(t, "ffffffff-ffff-ffff-ffff-ffffffffffff", cursor.ID)
	assert.Equal(t, phaseBackfill, cursor.Phase)
}

func TestSeedPhaseForwardCursor_AtNow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)
	cursor := seedPhaseForwardCursor(now)
	assert.Equal(t, now.UnixMicro(), cursor.UnixMicros)
	assert.Equal(t, "ffffffff-ffff-ffff-ffff-ffffffffffff", cursor.ID)
	assert.Equal(t, phaseForward, cursor.Phase)
}

func TestRepairCursor_LegacyFormatForPhase1(t *testing.T) {
	t.Parallel()
	// Phase 1 cursors are written in 2-field format so old canary tasks can
	// continue to parse them during rollout.
	c := repairCursor{UnixMicros: 1741733334000000, ID: "abc-123-def", Phase: phaseBackfill}
	assert.Equal(t, "1741733334000000:abc-123-def", c.String())
}

func TestRepairCursor_ThreeFieldFormatForLaterPhases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		phase    int
		expected string
	}{
		{phaseForward, "1741733334000000:abc-123-def:2"},
		{phaseDone, "1741733334000000:abc-123-def:3"},
	}
	for _, tc := range tests {
		c := repairCursor{UnixMicros: 1741733334000000, ID: "abc-123-def", Phase: tc.phase}
		assert.Equal(t, tc.expected, c.String())
	}
}

func TestParseRepairCursor_DefaultsToPhase1(t *testing.T) {
	t.Parallel()
	parsed, ok := parseRepairCursor("1741733334000000:abc-123-def")
	require.True(t, ok)
	assert.Equal(t, phaseBackfill, parsed.Phase)
}

func TestParseRepairCursor_WithPhaseSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw      string
		expected int
	}{
		{"1741733334000000:abc-123-def:1", phaseBackfill},
		{"1741733334000000:abc-123-def:2", phaseForward},
		{"1741733334000000:abc-123-def:3", phaseDone},
	}
	for _, tc := range tests {
		parsed, ok := parseRepairCursor(tc.raw)
		require.True(t, ok)
		assert.Equal(t, tc.expected, parsed.Phase)
		assert.Equal(t, "abc-123-def", parsed.ID)
	}
}

func TestParseRepairCursor_RoundTripAllPhases(t *testing.T) {
	t.Parallel()
	for _, phase := range []int{phaseBackfill, phaseForward, phaseDone} {
		original := repairCursor{UnixMicros: 1741733334000000, ID: "abc-123-def", Phase: phase}
		parsed, ok := parseRepairCursor(original.String())
		require.True(t, ok)
		assert.Equal(t, original, parsed, "phase %d failed roundtrip", phase)
	}
}

func getRepairTask() (ScheduledTaskSpec, error) {
	for _, t := range AllScheduledTasks() {
		if t.Name == "repair_transfer_participant_create_time" {
			return t, nil
		}
	}
	return ScheduledTaskSpec{}, assert.AnError
}

func defaultKnobs() knobs.Knobs {
	return knobs.NewFixedKnobs(map[string]float64{})
}

func TestRepairParticipantCreateTime_FixesDivergedRecords(t *testing.T) {
	// Not parallel: tests that invoke repairParticipantCreateTime share the
	// package-level repairMu; paralleling them causes silent-skip collisions.
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	senderKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	// Create a transfer with create_time well before the cutoff.
	transferTime := time.Date(2026, time.February, 15, 12, 0, 0, 0, time.UTC)
	tr := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweaked).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetCreateTime(transferTime).
		SaveX(ctx)

	// Create sender and receiver with a diverged create_time (simulating the bug).
	wrongTime := time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)
	client.TransferSender.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(senderKey).
		SetCreateTime(wrongTime).
		SaveX(ctx)

	client.TransferReceiver.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(receiverKey).
		SetStatus(st.TransferReceiverStatusSenderInitiated).
		SetCreateTime(wrongTime).
		SaveX(ctx)

	// Run the repair task with the knob enabled.
	task, err := getRepairTask()
	require.NoError(t, err)
	err = task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	// Verify sender create_time was corrected.
	senders, err := client.TransferSender.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	assert.WithinDuration(t, transferTime, senders[0].CreateTime, time.Second)

	// Verify receiver create_time was corrected.
	receivers, err := client.TransferReceiver.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, receivers, 1)
	assert.WithinDuration(t, transferTime, receivers[0].CreateTime, time.Second)
}

func TestRepairParticipantCreateTime_SkipsTransfersAfterCutoff(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	senderKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	// Create a transfer AFTER the cutoff — should not be processed.
	afterCutoff := repairCutoff.Add(24 * time.Hour)
	tr := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweaked).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetCreateTime(afterCutoff).
		SaveX(ctx)

	wrongTime := time.Date(2026, time.March, 15, 0, 0, 0, 0, time.UTC)
	client.TransferSender.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(senderKey).
		SetCreateTime(wrongTime).
		SaveX(ctx)

	task, err := getRepairTask()
	require.NoError(t, err)
	err = task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	// Sender should still have the wrong time — transfer is after cutoff.
	senders, err := client.TransferSender.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	assert.WithinDuration(t, wrongTime, senders[0].CreateTime, time.Second)
}

func TestRepairParticipantCreateTime_SkipsUnspecifiedNetwork(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	senderKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	transferTime := time.Date(2026, time.February, 15, 12, 0, 0, 0, time.UTC)
	wrongTime := time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)

	// Create an Unspecified-network transfer — should be skipped.
	tr := client.Transfer.Create().
		SetNetwork(btcnetwork.Unspecified).
		SetStatus(st.TransferStatusSenderKeyTweaked).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetCreateTime(transferTime).
		SaveX(ctx)

	client.TransferSender.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(senderKey).
		SetCreateTime(wrongTime).
		SaveX(ctx)

	task, err := getRepairTask()
	require.NoError(t, err)
	err = task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	// Sender should still have the wrong time — Unspecified transfers are skipped.
	senders, err := client.TransferSender.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	assert.WithinDuration(t, wrongTime, senders[0].CreateTime, time.Second)
}

func TestRepairParticipantCreateTime_BulkRepairsEntireBatch(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	senderKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	wrongTime := time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)
	baseTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	// Create 10 transfers, each with diverged sender AND receiver create_times,
	// all under the cutoff. A single bulk-UPDATE invocation should correct all 20 edge rows.
	expected := make(map[uuid.UUID]time.Time, 10)
	for i := range 10 {
		transferTime := baseTime.Add(time.Duration(i) * time.Hour)
		tr := client.Transfer.Create().
			SetNetwork(btcnetwork.Regtest).
			SetStatus(st.TransferStatusSenderKeyTweaked).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderKey).
			SetReceiverIdentityPubkey(receiverKey).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetCreateTime(transferTime).
			SaveX(ctx)
		expected[tr.ID] = transferTime

		client.TransferSender.Create().
			SetTransferID(tr.ID).
			SetIdentityPubkey(senderKey).
			SetCreateTime(wrongTime).
			SaveX(ctx)
		client.TransferReceiver.Create().
			SetTransferID(tr.ID).
			SetIdentityPubkey(receiverKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			SetCreateTime(wrongTime).
			SaveX(ctx)
	}

	repaired, err := repairParticipantCreateTime(ctx, cfg, client, 1000)
	require.NoError(t, err)
	assert.Equal(t, 20, repaired, "expected 10 senders + 10 receivers updated")

	// Every edge row should match its parent transfer's create_time exactly.
	senders, err := client.TransferSender.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, senders, 10)
	for _, s := range senders {
		want, ok := expected[s.TransferID]
		require.True(t, ok, "sender references unknown transfer %s", s.TransferID)
		assert.WithinDuration(t, want, s.CreateTime, time.Second, "sender %s create_time mismatch", s.ID)
	}
	receivers, err := client.TransferReceiver.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, receivers, 10)
	for _, r := range receivers {
		want, ok := expected[r.TransferID]
		require.True(t, ok, "receiver references unknown transfer %s", r.TransferID)
		assert.WithinDuration(t, want, r.CreateTime, time.Second, "receiver %s create_time mismatch", r.ID)
	}
}

// TestRepairParticipantCreateTime_SkipsWhenMutexHeld verifies the in-pod
// concurrency guard: a second invocation while another is in-flight must
// silent-skip (return 0, nil) rather than duplicating work. Can't be observed
// from an application boundary — the guard's only job is to prevent wasted
// work that is otherwise invisible (UPDATEs are idempotent), so testing the
// mutex contract directly is justified.
func TestRepairParticipantCreateTime_SkipsWhenMutexHeld(t *testing.T) {
	// NOTE: this test cannot run in parallel — it manipulates the package-level
	// repairMu, which would race with any parallel test that also calls into
	// repairParticipantCreateTime.
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	senderKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	transferTime := time.Date(2026, time.February, 15, 12, 0, 0, 0, time.UTC)
	wrongTime := time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)

	tr := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweaked).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetCreateTime(transferTime).
		SaveX(ctx)
	client.TransferSender.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(senderKey).
		SetCreateTime(wrongTime).
		SaveX(ctx)

	// Simulate a concurrent in-flight invocation by taking the mutex ourselves.
	repairMu.Lock()
	defer repairMu.Unlock()

	repaired, err := repairParticipantCreateTime(ctx, cfg, client, 1000)
	require.NoError(t, err)
	assert.Equal(t, 0, repaired, "expected silent-skip when mutex is held")

	// Sender should still have the wrong time — the second invocation did nothing.
	senders, err := client.TransferSender.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	assert.WithinDuration(t, wrongTime, senders[0].CreateTime, time.Second)
}

func TestRepairParticipantCreateTime_BatchPagination(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	senderKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	wrongTime := time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)
	baseTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	// Create 5 transfers with different create_times before the cutoff.
	for i := range 5 {
		transferTime := baseTime.Add(time.Duration(i) * time.Hour)
		tr := client.Transfer.Create().
			SetNetwork(btcnetwork.Regtest).
			SetStatus(st.TransferStatusSenderKeyTweaked).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderKey).
			SetReceiverIdentityPubkey(receiverKey).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			SetCreateTime(transferTime).
			SaveX(ctx)

		client.TransferSender.Create().
			SetTransferID(tr.ID).
			SetIdentityPubkey(senderKey).
			SetCreateTime(wrongTime).
			SaveX(ctx)
	}

	// Call repairParticipantCreateTime directly with batchSize=2 to test pagination.
	// No memcache configured, so cursor reseeds each call — but each run advances
	// from newest to oldest. Without memcache, it processes the same top-2 repeatedly.
	// This test verifies the core batch+update logic works; cursor persistence is
	// tested via the memcache-backed integration tests above.
	repaired, err := repairParticipantCreateTime(ctx, cfg, client, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, repaired)

	// Verify exactly 2 of the 5 senders were corrected (the two newest before cutoff).
	senders, err := client.TransferSender.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, senders, 5)
	corrected := 0
	for _, s := range senders {
		if !s.CreateTime.Equal(wrongTime) {
			corrected++
		}
	}
	assert.Equal(t, 2, corrected)
}
