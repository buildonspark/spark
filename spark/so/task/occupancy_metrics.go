package task

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transfer"
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

var (
	occupancyMeter         = otel.Meter("occupancy")
	transferOccupancyGauge metric.Int64Gauge
	transferOldestAgeGauge metric.Float64Gauge

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

// clampAge guards against update_time values ahead of the task's clock
// (clock skew between the SO and Postgres would otherwise emit negative ages).
func clampAge(age time.Duration) time.Duration {
	if age < 0 {
		return 0
	}
	return age
}
