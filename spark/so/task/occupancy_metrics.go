package task

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/lightsparkdev/spark/so/ent/treenode"
)

type transferCellKey struct {
	network      btcnetwork.Network
	status       st.TransferStatus
	transferType st.TransferType
}

type occupancyCell struct {
	count     int64
	oldestAge time.Duration
}

// transferOccupancyRow's JSON tags must match Ent's column aliases: grouped
// fields keep their column names; "count"/"min" are the default aliases for
// ent.Count() and ent.Min(...) (same contract as inFlightAggregateRow in
// flow_execution_reconcile.go).
type transferOccupancyRow struct {
	Network btcnetwork.Network `json:"network"`
	Status  st.TransferStatus  `json:"status"`
	Type    st.TransferType    `json:"type"`
	Count   int64              `json:"count"`
	Min     time.Time          `json:"min"`
}

type transferReceiverCellKey struct {
	network      btcnetwork.Network
	status       st.TransferReceiverStatus
	transferType st.TransferType
}

// transferReceiverOccupancyRow's aliases are set explicitly in the Modify
// select (the network join means Ent's default GroupBy aliases don't apply).
type transferReceiverOccupancyRow struct {
	Network btcnetwork.Network        `json:"network"`
	Status  st.TransferReceiverStatus `json:"status"`
	Type    st.TransferType           `json:"transfer_type"`
	Count   int64                     `json:"count"`
	Min     time.Time                 `json:"min"`
}

type treeNodeCellKey struct {
	network btcnetwork.Network
	status  st.TreeNodeStatus
}

type treeNodeOccupancyRow struct {
	Network btcnetwork.Network `json:"network"`
	Status  st.TreeNodeStatus  `json:"status"`
	Count   int64              `json:"count"`
	Min     time.Time          `json:"min"`
}

var (
	occupancyMeter                 = otel.Meter("occupancy")
	transferOccupancyGauge         metric.Int64Gauge
	transferOldestAgeGauge         metric.Float64Gauge
	transferReceiverOccupancyGauge metric.Int64Gauge
	transferReceiverOldestAgeGauge metric.Float64Gauge
	treeNodeOccupancyGauge         metric.Int64Gauge
	treeNodeOldestAgeGauge         metric.Float64Gauge

	publishOccupancyMetricsTimeout = 2 * time.Minute
)

func init() {
	var err error
	transferOccupancyGauge, err = occupancyMeter.Int64Gauge(
		"spark_transfer_occupancy",
		metric.WithDescription("Transfers currently in each non-terminal status, by status/network/type. Zero-filled: a drained status reports 0."),
		metric.WithUnit("{row}"),
	)
	if err != nil {
		otel.Handle(err)
		transferOccupancyGauge = noop.Int64Gauge{}
	}

	transferOldestAgeGauge, err = occupancyMeter.Float64Gauge(
		"spark_transfer_oldest_age_seconds",
		metric.WithDescription("Age in seconds of the oldest transfer (now - MIN(update_time)) per non-terminal status; 0 when the status is empty."),
	)
	if err != nil {
		otel.Handle(err)
		transferOldestAgeGauge = noop.Float64Gauge{}
	}

	transferReceiverOccupancyGauge, err = occupancyMeter.Int64Gauge(
		"spark_transfer_receiver_occupancy",
		metric.WithDescription("Transfer receivers currently in each non-terminal claim status, by status/network/type."),
		metric.WithUnit("{row}"),
	)
	if err != nil {
		otel.Handle(err)
		transferReceiverOccupancyGauge = noop.Int64Gauge{}
	}

	transferReceiverOldestAgeGauge, err = occupancyMeter.Float64Gauge(
		"spark_transfer_receiver_oldest_age_seconds",
		metric.WithDescription("Age in seconds of the oldest transfer receiver (now - MIN(update_time)) per non-terminal claim status; 0 when the status is empty."),
	)
	if err != nil {
		otel.Handle(err)
		transferReceiverOldestAgeGauge = noop.Float64Gauge{}
	}

	treeNodeOccupancyGauge, err = occupancyMeter.Int64Gauge(
		"spark_tree_node_occupancy",
		metric.WithDescription("Tree nodes currently in each occupancy-tracked status, by status/network. Zero-filled: a drained status reports 0."),
		metric.WithUnit("{row}"),
	)
	if err != nil {
		otel.Handle(err)
		treeNodeOccupancyGauge = noop.Int64Gauge{}
	}

	treeNodeOldestAgeGauge, err = occupancyMeter.Float64Gauge(
		"spark_tree_node_oldest_age_seconds",
		metric.WithDescription("Age in seconds of the oldest tree node (now - MIN(update_time)) per occupancy-tracked status; 0 when the status is empty."),
	)
	if err != nil {
		otel.Handle(err)
		treeNodeOldestAgeGauge = noop.Float64Gauge{}
	}
}

func publishOccupancyMetrics(ctx context.Context, config *so.Config) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	now := time.Now()

	transferCells, err := transferOccupancyCells(ctx, db, config.SupportedNetworks, now)
	if err != nil {
		return err
	}
	for k, c := range transferCells {
		attrs := metric.WithAttributes(
			attribute.String("status", string(k.status)),
			attribute.String("network", k.network.String()),
			attribute.String("type", string(k.transferType)),
		)
		transferOccupancyGauge.Record(ctx, c.count, attrs)
		transferOldestAgeGauge.Record(ctx, c.oldestAge.Seconds(), attrs)
	}

	receiverCells, err := transferReceiverOccupancyCells(ctx, db, config.SupportedNetworks, now)
	if err != nil {
		return err
	}
	for k, c := range receiverCells {
		attrs := metric.WithAttributes(
			attribute.String("status", string(k.status)),
			attribute.String("network", k.network.String()),
			attribute.String("type", string(k.transferType)),
		)
		transferReceiverOccupancyGauge.Record(ctx, c.count, attrs)
		transferReceiverOldestAgeGauge.Record(ctx, c.oldestAge.Seconds(), attrs)
	}

	treeNodeCells, err := treeNodeOccupancyCells(ctx, db, config.SupportedNetworks, now)
	if err != nil {
		return err
	}
	for k, c := range treeNodeCells {
		attrs := metric.WithAttributes(
			attribute.String("status", string(k.status)),
			attribute.String("network", k.network.String()),
		)
		treeNodeOccupancyGauge.Record(ctx, c.count, attrs)
		treeNodeOldestAgeGauge.Record(ctx, c.oldestAge.Seconds(), attrs)
	}
	return nil
}

// transferOccupancyCells returns one cell per (network × non-terminal status
// × type), zero-filled so a drained status reads as a real 0 rather than a
// missing series. Rows on networks outside the passed set still emit.
func transferOccupancyCells(ctx context.Context, db *ent.Client, networks []btcnetwork.Network, now time.Time) (map[transferCellKey]occupancyCell, error) {
	cells := make(map[transferCellKey]occupancyCell)
	for _, network := range networks {
		for _, status := range st.NonTerminalTransferStatuses() {
			for _, transferType := range (st.TransferType("")).Values() {
				cells[transferCellKey{network, status, st.TransferType(transferType)}] = occupancyCell{}
			}
		}
	}

	var rows []transferOccupancyRow
	if err := db.Transfer.Query().
		Where(transfer.StatusIn(st.NonTerminalTransferStatuses()...)).
		GroupBy(transfer.FieldNetwork, transfer.FieldStatus, transfer.FieldType).
		Aggregate(ent.Count(), ent.Min(transfer.FieldUpdateTime)).
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("aggregate transfer occupancy: %w", err)
	}
	for _, r := range rows {
		cells[transferCellKey{r.Network, r.Status, r.Type}] = occupancyCell{
			count:     r.Count,
			oldestAge: clampAge(now.Sub(r.Min)),
		}
	}
	return cells, nil
}

// transferReceiverOccupancyCells is the transfer_receivers twin of
// transferOccupancyCells. transfer_receivers carries no network column, so
// the aggregate joins the parent transfer for it.
func transferReceiverOccupancyCells(ctx context.Context, db *ent.Client, networks []btcnetwork.Network, now time.Time) (map[transferReceiverCellKey]occupancyCell, error) {
	cells := make(map[transferReceiverCellKey]occupancyCell)
	for _, network := range networks {
		for _, status := range st.NonTerminalTransferReceiverStatuses() {
			for _, transferType := range (st.TransferType("")).Values() {
				cells[transferReceiverCellKey{network, status, st.TransferType(transferType)}] = occupancyCell{}
			}
		}
	}

	var rows []transferReceiverOccupancyRow
	if err := db.TransferReceiver.Query().
		Where(transferreceiver.StatusIn(st.NonTerminalTransferReceiverStatuses()...)).
		Modify(func(s *sql.Selector) {
			t := sql.Table(transfer.Table)
			s.Join(t).On(s.C(transferreceiver.FieldTransferID), t.C(transfer.FieldID))
			s.Select(
				sql.As(t.C(transfer.FieldNetwork), "network"),
				sql.As(s.C(transferreceiver.FieldStatus), "status"),
				sql.As(s.C(transferreceiver.FieldTransferType), "transfer_type"),
				sql.As(sql.Count("*"), "count"),
				sql.As(sql.Min(s.C(transferreceiver.FieldUpdateTime)), "min"),
			)
			s.GroupBy(
				t.C(transfer.FieldNetwork),
				s.C(transferreceiver.FieldStatus),
				s.C(transferreceiver.FieldTransferType),
			)
		}).
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("aggregate transfer_receiver occupancy: %w", err)
	}
	for _, r := range rows {
		cells[transferReceiverCellKey{r.Network, r.Status, r.Type}] = occupancyCell{
			count:     r.Count,
			oldestAge: clampAge(now.Sub(r.Min)),
		}
	}
	return cells, nil
}

// treeNodeOccupancyCells is the tree_nodes twin of transferOccupancyCells
// (no type dimension).
func treeNodeOccupancyCells(ctx context.Context, db *ent.Client, networks []btcnetwork.Network, now time.Time) (map[treeNodeCellKey]occupancyCell, error) {
	cells := make(map[treeNodeCellKey]occupancyCell)
	for _, network := range networks {
		for _, status := range st.OccupancyTreeNodeStatuses() {
			cells[treeNodeCellKey{network, status}] = occupancyCell{}
		}
	}

	var rows []treeNodeOccupancyRow
	if err := db.TreeNode.Query().
		Where(treenode.StatusIn(st.OccupancyTreeNodeStatuses()...)).
		GroupBy(treenode.FieldNetwork, treenode.FieldStatus).
		Aggregate(ent.Count(), ent.Min(treenode.FieldUpdateTime)).
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("aggregate tree_node occupancy: %w", err)
	}
	for _, r := range rows {
		cells[treeNodeCellKey{r.Network, r.Status}] = occupancyCell{
			count:     r.Count,
			oldestAge: clampAge(now.Sub(r.Min)),
		}
	}
	return cells, nil
}

// clampAge guards against update_time values ahead of the task's clock
// (clock skew between the SO and Postgres would otherwise emit negative ages).
func clampAge(age time.Duration) time.Duration {
	if age < 0 {
		return 0
	}
	return age
}
