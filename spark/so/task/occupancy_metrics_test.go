package task

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestTransferOccupancyCells_CountsNonTerminalAndZeroFills(t *testing.T) {
	t.Parallel()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	rng := rand.NewChaCha8([32]byte{91})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// transferA is current; transferB sits behind the cutoff, so a merged cell
	// would show up as a count of 2 in one cohort instead of 1 in each. Seeding
	// off the cutoff rather than time.Now() keeps the cohort assignment
	// independent of when the suite runs.
	transferA, _ := createOccupancyTransferAt(t, ctx, client, st.TransferTypeTransfer,
		st.TransferStatusSenderInitiated, sender, receiver, occupancyCohortCutoff.Add(24*time.Hour))
	transferB, _ := createOccupancyTransferAt(t, ctx, client, st.TransferTypeTransfer,
		st.TransferStatusSenderInitiated, sender, receiver,
		occupancyCohortCutoff.Add(-24*time.Hour))
	createOccupancyTransferAt(t, ctx, client, st.TransferTypeCooperativeExit,
		st.TransferStatusCompleted, sender, receiver, occupancyCohortCutoff.Add(24*time.Hour))

	now := time.Now()
	cells, err := transferOccupancyCells(ctx, client, []btcnetwork.Network{btcnetwork.Regtest}, now)
	require.NoError(t, err)

	// Full zero-filled cross product: every non-terminal status × every type × every cohort.
	assert.Len(t, cells, len(st.NonTerminalTransferStatuses())*
		len((st.TransferType("")).Values())*
		len(occupancyCohortValues()))

	current := cells[transferCellKey{
		network:      btcnetwork.Regtest,
		status:       transferA.Status,
		transferType: st.TransferTypeTransfer,
		cohort:       occupancyCohortCurrent,
	}]
	assert.EqualValues(t, 1, current.count)
	assert.InDelta(t, now.Sub(transferA.UpdateTime).Seconds(), current.oldestAge.Seconds(), 5.0)

	legacy := cells[transferCellKey{
		network:      btcnetwork.Regtest,
		status:       transferB.Status,
		transferType: st.TransferTypeTransfer,
		cohort:       occupancyCohortLegacy,
	}]
	assert.EqualValues(t, 1, legacy.count)
	// The cohort keys off create_time while the age gauge reads update_time, so
	// a legacy row still reports a fresh age — the two dimensions are independent.
	assert.InDelta(t, now.Sub(transferB.UpdateTime).Seconds(), legacy.oldestAge.Seconds(), 5.0)

	// Terminal rows are invisible: the COMPLETED coop exit contributes nowhere,
	// so its (status, type) cell does not exist and untouched cells are zero.
	_, exists := cells[transferCellKey{
		network:      btcnetwork.Regtest,
		status:       st.TransferStatusCompleted,
		transferType: st.TransferTypeCooperativeExit,
		cohort:       occupancyCohortCurrent,
	}]
	assert.False(t, exists)
	empty := cells[transferCellKey{
		network:      btcnetwork.Regtest,
		status:       st.TransferStatusReceiverRefundSigned,
		transferType: st.TransferTypeSwap,
		cohort:       occupancyCohortLegacy,
	}]
	assert.EqualValues(t, 0, empty.count)
	assert.Equal(t, time.Duration(0), empty.oldestAge)
}

func TestTransferReceiverOccupancyCells_CountsNonTerminalAndZeroFills(t *testing.T) {
	t.Parallel()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	rng := rand.NewChaCha8([32]byte{93})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverA := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverB := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	rowACreate := occupancyCohortCutoff.Add(24 * time.Hour)
	_, rowA := createOccupancyTransferAt(t, ctx, client, st.TransferTypeTransfer,
		st.TransferStatusSenderInitiated, sender, receiverA, rowACreate)
	createOccupancyTransferAt(t, ctx, client, st.TransferTypeTransfer,
		st.TransferStatusSenderInitiated, sender, receiverB,
		occupancyCohortCutoff.Add(-24*time.Hour))

	// A terminal receiver row must contribute nowhere, even in the current cohort.
	_, completedRow := createOccupancyTransferAt(t, ctx, client, st.TransferTypeCooperativeExit,
		st.TransferStatusSenderInitiated, sender, receiverA, occupancyCohortCutoff.Add(24*time.Hour))
	_, err := completedRow.Update().SetStatus(st.TransferReceiverStatusCompleted).Save(ctx)
	require.NoError(t, err)

	now := time.Now()
	cells, err := transferReceiverOccupancyCells(ctx, client, []btcnetwork.Network{btcnetwork.Regtest}, now)
	require.NoError(t, err)

	// Pin the non-terminal set to its literal members: a cell-count check
	// derived from the helper alone would silently absorb a terminal status
	// (COMPLETED or CANCELLED) being reclassified as non-terminal.
	assert.ElementsMatch(t, []st.TransferReceiverStatus{
		st.TransferReceiverStatusInitiated,
		st.TransferReceiverStatusReceiverClaimPending,
		st.TransferReceiverStatusKeyTweaked,
		st.TransferReceiverStatusKeyTweakLocked,
		st.TransferReceiverStatusKeyTweakApplied,
		st.TransferReceiverStatusRefundSigned,
	}, st.NonTerminalTransferReceiverStatuses())
	assert.Len(t, cells, len(st.NonTerminalTransferReceiverStatuses())*
		len((st.TransferType("")).Values())*
		len(occupancyCohortValues()))

	// The network dimension comes from the parent-transfer join, so a
	// populated Regtest cell proves the join wiring, not just the count.
	current := cells[transferReceiverCellKey{
		network:      btcnetwork.Regtest,
		status:       st.TransferReceiverStatusInitiated,
		transferType: st.TransferTypeTransfer,
		cohort:       occupancyCohortCurrent,
	}]
	assert.EqualValues(t, 1, current.count)
	assert.InDelta(t, now.Sub(rowA.UpdateTime).Seconds(), current.oldestAge.Seconds(), 5.0)

	legacy := cells[transferReceiverCellKey{
		network:      btcnetwork.Regtest,
		status:       st.TransferReceiverStatusInitiated,
		transferType: st.TransferTypeTransfer,
		cohort:       occupancyCohortLegacy,
	}]
	assert.EqualValues(t, 1, legacy.count)

	_, exists := cells[transferReceiverCellKey{
		network:      btcnetwork.Regtest,
		status:       st.TransferReceiverStatusCompleted,
		transferType: st.TransferTypeCooperativeExit,
		cohort:       occupancyCohortCurrent,
	}]
	assert.False(t, exists)
	empty := cells[transferReceiverCellKey{
		network:      btcnetwork.Regtest,
		status:       st.TransferReceiverStatusKeyTweaked,
		transferType: st.TransferTypeSwap,
		cohort:       occupancyCohortLegacy,
	}]
	assert.EqualValues(t, 0, empty.count)
	assert.Equal(t, time.Duration(0), empty.oldestAge)
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

// TestPublishOccupancyMetricsTask_RecordsCohortAttribute observes the metrics
// publishOccupancyMetrics actually emits rather than the aggregation helpers:
// every other test in this file only checks the intermediate cells map, so a
// cohort attribute dropped from a Record call site would leave them all passing.
// Not parallel: it swaps the package's gauge vars for the duration of the test.
func TestPublishOccupancyMetricsTask_RecordsCohortAttribute(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	rng := rand.NewChaCha8([32]byte{96})
	sender := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	createOccupancyTransferAt(t, ctx, client, st.TransferTypeTransfer,
		st.TransferStatusSenderInitiated, sender, receiver,
		occupancyCohortCutoff.Add(24*time.Hour))
	createOccupancyTransferAt(t, ctx, client, st.TransferTypeTransfer,
		st.TransferStatusSenderInitiated, sender, receiver,
		occupancyCohortCutoff.Add(-24*time.Hour))

	reader := installOccupancyMetricsTestMeter(t)

	spec := getScheduledTaskByName(t, "publish_occupancy_metrics")
	cfg := sparktesting.TestConfig(t)
	require.NoError(t, spec.RunOnce(ctx, cfg, client, nil, knobs.NewFixedKnobs(nil)))

	cohorts := collectGaugeCohorts(t, reader, "spark_transfer_occupancy")
	assert.Contains(t, cohorts, string(occupancyCohortCurrent))
	assert.Contains(t, cohorts, string(occupancyCohortLegacy))
}

// installOccupancyMetricsTestMeter points the package's occupancy gauges at a
// fresh in-memory reader. The gauges are package vars resolved against
// whichever provider wins the first otel.SetMeterProvider call in this test
// binary (see reconcile_signing_keyshare_secret_pointers_test.go's init()), so
// a second SetMeterProvider call here cannot rebind the existing instruments —
// only ones created fresh afterward pick up the new provider, which is why
// every gauge var is recreated below rather than reused.
func installOccupancyMetricsTestMeter(t *testing.T) *msdk.ManualReader {
	t.Helper()

	reader := msdk.NewManualReader()
	prevProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(msdk.NewMeterProvider(msdk.WithReader(reader)))
	meter := otel.Meter("occupancy")

	prevTransferOccupancyGauge := transferOccupancyGauge
	prevTransferOldestAgeGauge := transferOldestAgeGauge
	prevTransferReceiverOccupancyGauge := transferReceiverOccupancyGauge
	prevTransferReceiverOldestAgeGauge := transferReceiverOldestAgeGauge
	prevTreeNodeOccupancyGauge := treeNodeOccupancyGauge
	prevTreeNodeOldestAgeGauge := treeNodeOldestAgeGauge

	var err error
	transferOccupancyGauge, err = meter.Int64Gauge("spark_transfer_occupancy")
	require.NoError(t, err)
	transferOldestAgeGauge, err = meter.Float64Gauge("spark_transfer_oldest_age_seconds")
	require.NoError(t, err)
	transferReceiverOccupancyGauge, err = meter.Int64Gauge("spark_transfer_receiver_occupancy")
	require.NoError(t, err)
	transferReceiverOldestAgeGauge, err = meter.Float64Gauge("spark_transfer_receiver_oldest_age_seconds")
	require.NoError(t, err)
	treeNodeOccupancyGauge, err = meter.Int64Gauge("spark_tree_node_occupancy")
	require.NoError(t, err)
	treeNodeOldestAgeGauge, err = meter.Float64Gauge("spark_tree_node_oldest_age_seconds")
	require.NoError(t, err)

	t.Cleanup(func() {
		transferOccupancyGauge = prevTransferOccupancyGauge
		transferOldestAgeGauge = prevTransferOldestAgeGauge
		transferReceiverOccupancyGauge = prevTransferReceiverOccupancyGauge
		transferReceiverOldestAgeGauge = prevTransferReceiverOldestAgeGauge
		treeNodeOccupancyGauge = prevTreeNodeOccupancyGauge
		treeNodeOldestAgeGauge = prevTreeNodeOldestAgeGauge
		otel.SetMeterProvider(prevProvider)
	})

	return reader
}

// collectGaugeCohorts returns every "cohort" attribute value recorded across
// the named gauge's data points.
func collectGaugeCohorts(t *testing.T, reader *msdk.ManualReader, gaugeName string) []string {
	t.Helper()

	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &rm))

	var cohorts []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != gaugeName {
				continue
			}
			gauge, ok := m.Data.(md.Gauge[int64])
			require.True(t, ok, "%s should be an int64 gauge", gaugeName)
			for _, dp := range gauge.DataPoints {
				cohort, ok := dp.Attributes.Value(attribute.Key("cohort"))
				require.True(t, ok, "%s data point missing cohort attribute", gaugeName)
				cohorts = append(cohorts, cohort.AsString())
			}
		}
	}
	return cohorts
}

func TestTreeNodeOccupancyCells_CountsTrackedStatusesAndZeroFills(t *testing.T) {
	t.Parallel()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	rng := rand.NewChaCha8([32]byte{92})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createSenderInitiatedExpirySigningKeyshare(t, ctx, rng, client)
	tree := createSenderInitiatedExpiryTree(t, ctx, owner, client)

	// Only the TRANSFER_LOCKED leaf is legacy, so its status is the one place a
	// cohort mix-up would show as a count in the wrong bucket.
	createOccupancyLeafAt(t, ctx, rng, client, tree, keyshare, nil,
		st.TreeNodeStatusTransferLocked, occupancyCohortCutoff.Add(-24*time.Hour))
	available := createOccupancyLeafAt(t, ctx, rng, client, tree, keyshare, nil,
		st.TreeNodeStatusAvailable, occupancyCohortCutoff.Add(24*time.Hour))
	createOccupancyLeafAt(t, ctx, rng, client, tree, keyshare, nil,
		st.TreeNodeStatusSplitted, occupancyCohortCutoff.Add(24*time.Hour))
	// A parented node lands in the child bucket, not root.
	createOccupancyLeafAt(t, ctx, rng, client, tree, keyshare, available,
		st.TreeNodeStatusCreating, occupancyCohortCutoff.Add(24*time.Hour))

	now := time.Now()
	cells, err := treeNodeOccupancyCells(ctx, client, []btcnetwork.Network{btcnetwork.Regtest}, now)
	require.NoError(t, err)

	assert.Len(t, cells,
		len(st.OccupancyTreeNodeStatuses())*len(occupancyCohortValues())*len(treeNodeKindValues()))

	locked := cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusTransferLocked,
		cohort:  occupancyCohortLegacy,
		kind:    treeNodeKindRoot,
	}]
	assert.EqualValues(t, 1, locked.count)
	assert.Greater(t, locked.oldestAge, time.Duration(0))

	// The same status in the other cohort zero-fills rather than absorbing the row.
	lockedCurrent := cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusTransferLocked,
		cohort:  occupancyCohortCurrent,
		kind:    treeNodeKindRoot,
	}]
	assert.EqualValues(t, 0, lockedCurrent.count)

	creatingChild := cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusCreating,
		cohort:  occupancyCohortCurrent,
		kind:    treeNodeKindChild,
	}]
	assert.EqualValues(t, 1, creatingChild.count)

	// The root bucket of the same status zero-fills rather than absorbing the child.
	creatingRoot := cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusCreating,
		cohort:  occupancyCohortCurrent,
		kind:    treeNodeKindRoot,
	}]
	assert.EqualValues(t, 0, creatingRoot.count)

	// Neither the terminal SPLITTED leaf nor the resting AVAILABLE leaf
	// contributes a cell.
	_, exists := cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusSplitted,
		cohort:  occupancyCohortLegacy,
		kind:    treeNodeKindRoot,
	}]
	assert.False(t, exists)
	_, exists = cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusAvailable,
		cohort:  occupancyCohortCurrent,
		kind:    treeNodeKindRoot,
	}]
	assert.False(t, exists)

	empty := cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusRenewLocked,
		cohort:  occupancyCohortCurrent,
		kind:    treeNodeKindRoot,
	}]
	assert.EqualValues(t, 0, empty.count)
}

func TestTreeNodeOccupancyCells_CutoffBoundaryIsCurrent(t *testing.T) {
	t.Parallel()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	rng := rand.NewChaCha8([32]byte{94})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createSenderInitiatedExpirySigningKeyshare(t, ctx, rng, client)
	tree := createSenderInitiatedExpiryTree(t, ctx, owner, client)

	createOccupancyLeafAt(t, ctx, rng, client, tree, keyshare, nil,
		st.TreeNodeStatusTransferLocked, occupancyCohortCutoff)
	createOccupancyLeafAt(t, ctx, rng, client, tree, keyshare, nil,
		st.TreeNodeStatusTransferLocked, occupancyCohortCutoff.Add(-time.Microsecond))

	cells, err := treeNodeOccupancyCells(
		ctx, client, []btcnetwork.Network{btcnetwork.Regtest}, time.Now())
	require.NoError(t, err)

	current := cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusTransferLocked,
		cohort:  occupancyCohortCurrent,
		kind:    treeNodeKindRoot,
	}]
	legacy := cells[treeNodeCellKey{
		network: btcnetwork.Regtest,
		status:  st.TreeNodeStatusTransferLocked,
		cohort:  occupancyCohortLegacy,
		kind:    treeNodeKindRoot,
	}]
	assert.EqualValues(t, 1, current.count, "a row exactly at the cutoff is current")
	assert.EqualValues(t, 1, legacy.count, "one microsecond before the cutoff is legacy")
}

// Duplicates the shared seed helpers rather than wrapping them: create_time is
// settable only at insert, and *ent.Client exposes no ExecContext, so a row
// seeded by those helpers can never be moved into the legacy cohort afterwards.
func createOccupancyTransferAt(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	transferType st.TransferType,
	status st.TransferStatus,
	sender keys.Public,
	receiver keys.Public,
	createTime time.Time,
) (*ent.Transfer, *ent.TransferReceiver) {
	t.Helper()

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(status).
		SetType(transferType).
		SetSenderIdentityPubkey(sender).
		SetReceiverIdentityPubkey(receiver).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(time.Hour)).
		SetCreateTime(createTime).
		Save(ctx)
	require.NoError(t, err)

	receiverRow, err := client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiver).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		SetCreateTime(createTime).
		Save(ctx)
	require.NoError(t, err)

	return transfer, receiverRow
}

func createOccupancyLeafAt(
	t *testing.T,
	ctx context.Context,
	rng *rand.ChaCha8,
	client *ent.Client,
	tree *ent.Tree,
	keyshare *ent.SigningKeyshare,
	parent *ent.TreeNode,
	status st.TreeNodeStatus,
	createTime time.Time,
) *ent.TreeNode {
	t.Helper()

	create := client.TreeNode.Create().
		SetStatus(status).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(keyshare).
		SetValue(1000).
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerIdentityPubkey(tree.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(senderInitiatedExpiryRawTxBytes(t, 1)).
		SetRawRefundTx(senderInitiatedExpiryRawTxBytes(t, 2)).
		SetDirectTx(senderInitiatedExpiryRawTxBytes(t, 1)).
		SetDirectRefundTx(senderInitiatedExpiryRawTxBytes(t, 3)).
		SetDirectFromCpfpRefundTx(senderInitiatedExpiryRawTxBytes(t, 4)).
		SetVout(0).
		SetCreateTime(createTime)
	if parent != nil {
		create = create.SetParent(parent)
	}
	leaf, err := create.Save(ctx)
	require.NoError(t, err)
	return leaf
}
