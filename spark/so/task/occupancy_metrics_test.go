package task

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransferOccupancyCells_CountsNonTerminalAndZeroFills(t *testing.T) {
	t.Parallel()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	rng := rand.NewChaCha8([32]byte{91})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transferA, _ := createSenderInitiatedExpiryTransferWithReceiver(
		t, ctx, client, st.TransferTypeTransfer, sender, receiver, time.Now().Add(time.Hour))
	transferB, _ := createSenderInitiatedExpiryTransferWithReceiver(
		t, ctx, client, st.TransferTypeTransfer, sender, receiver, time.Now().Add(time.Hour))
	completed, _ := createSenderInitiatedExpiryTransferWithReceiver(
		t, ctx, client, st.TransferTypeCooperativeExit, sender, receiver, time.Now().Add(time.Hour))
	_, err := completed.Update().SetStatus(st.TransferStatusCompleted).Save(ctx)
	require.NoError(t, err)

	now := time.Now()
	cells, err := transferOccupancyCells(ctx, client, []btcnetwork.Network{btcnetwork.Regtest}, now)
	require.NoError(t, err)

	// Full zero-filled cross product: every non-terminal status × every type.
	assert.Len(t, cells, len(st.NonTerminalTransferStatuses())*len((st.TransferType("")).Values()))

	got := cells[transferCellKey{
		network:      btcnetwork.Regtest,
		status:       transferA.Status,
		transferType: st.TransferTypeTransfer,
	}]
	assert.EqualValues(t, 2, got.count)
	assert.Greater(t, got.oldestAge, time.Duration(0))
	// oldestAge derives from the earlier of the two rows.
	assert.InDelta(t, now.Sub(transferA.UpdateTime).Seconds(), got.oldestAge.Seconds(), 5.0)

	// Terminal rows are invisible: the COMPLETED coop exit contributes nowhere,
	// so its (status, type) cell does not exist and untouched cells are zero.
	_, exists := cells[transferCellKey{
		network:      btcnetwork.Regtest,
		status:       st.TransferStatusCompleted,
		transferType: st.TransferTypeCooperativeExit,
	}]
	assert.False(t, exists)
	empty := cells[transferCellKey{
		network:      btcnetwork.Regtest,
		status:       st.TransferStatusReceiverRefundSigned,
		transferType: st.TransferTypeSwap,
	}]
	assert.EqualValues(t, 0, empty.count)
	assert.Equal(t, time.Duration(0), empty.oldestAge)

	_ = transferB
}

func TestPublishOccupancyMetricsTask_IsRegisteredAndRuns(t *testing.T) {
	t.Parallel()

	var spec *ScheduledTaskSpec
	for i, s := range AllScheduledTasks() {
		if s.Name == "publish_occupancy_metrics" {
			spec = &AllScheduledTasks()[i]
			break
		}
	}
	require.NotNil(t, spec, "publish_occupancy_metrics not registered in AllScheduledTasks")
	assert.Equal(t, 10*time.Minute, spec.ExecutionInterval)
	assert.True(t, spec.RunInTestEnv)

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	err := spec.RunOnce(ctx, cfg, sessionCtx.Client, nil, knobs.NewFixedKnobs(nil))
	require.NoError(t, err)
}
