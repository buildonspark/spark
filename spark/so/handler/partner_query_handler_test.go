package handler

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	pbpartner "github.com/lightsparkdev/spark/proto/spark_partner"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/partner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedTransactionVolumeMV creates the materialized view schema that exists in
// RisingWave (test Postgres stands in for it) and inserts a fixed dataset.
func seedTransactionVolumeMV(t *testing.T, dsn string) {
	t.Helper()
	pgDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer pgDB.Close()

	_, err = pgDB.ExecContext(t.Context(), `
		CREATE TABLE IF NOT EXISTS spark_transaction_volume_mv (
			partner_id TEXT NOT NULL,
			label TEXT NOT NULL,
			date TEXT NOT NULL,
			transaction_type TEXT NOT NULL,
			network TEXT NOT NULL,
			volume_sats BIGINT NOT NULL,
			transaction_count BIGINT NOT NULL
		)
	`)
	require.NoError(t, err)

	_, err = pgDB.ExecContext(t.Context(), `
		INSERT INTO spark_transaction_volume_mv (partner_id, label, date, transaction_type, network, volume_sats, transaction_count) VALUES
			('partner-a', 'label-1', '2025-03-01', 'TRANSFER',       'MAINNET', 50000, 10),
			('partner-a', 'label-1', '2025-03-02', 'TRANSFER',       'MAINNET', 30000, 5),
			('partner-a', 'label-1', '2025-03-01', 'LIGHTNING_SEND', 'MAINNET', 20000, 3),
			('partner-a', 'label-1', '2025-03-01', 'TRANSFER',       'REGTEST', 7000,  2),
			('partner-a', 'label-2', '2025-03-01', 'TRANSFER',       'MAINNET', 99999, 1),
			('partner-b', 'label-1', '2025-03-01', 'TRANSFER',       'MAINNET', 88888, 1)
	`)
	require.NoError(t, err)
}

func partnerCtx(ctx context.Context, partnerID string) context.Context {
	return partner.ContextWithPartnerInfo(ctx, &partner.PartnerInfo{PartnerID: partnerID})
}

// TestQuerySparkTransactionVolumes_NoLabel verifies that an unscoped query
// returns one entry per (label, transaction type) pair, ordered by label then
// type, with grand totals accumulated across every entry.
func TestQuerySparkTransactionVolumes_NoLabel(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	seedTransactionVolumeMV(t, tc.DatabasePath())

	client := partner.NewRisingWaveClient(tc.DatabasePath())
	require.NotNil(t, client)
	defer func() { _ = client.Close() }()

	h := NewPartnerQueryHandler(client)
	resp, err := h.QuerySparkTransactionVolumes(partnerCtx(ctx, "partner-a"), &pbpartner.QuerySparkTransactionVolumesRequest{
		StartDate: "2025-03-01",
		EndDate:   "2025-03-31",
	})
	require.NoError(t, err)

	assert.Equal(t, "partner-a", resp.GetPartnerId())
	assert.Equal(t, int64(206999), resp.GetTotalVolumeSats())
	assert.Equal(t, int64(21), resp.GetTotalTransactionCount())

	// Entries are ordered by (label, transaction_type); "LIGHTNING_SEND" sorts
	// before "TRANSFER".
	expectedEntries := []*pbpartner.TransactionVolumeEntry{
		{Label: "label-1", TransactionType: pbpartner.SparkTransactionType_SPARK_TRANSACTION_TYPE_LIGHTNING_SEND, VolumeSats: 20000, TransactionCount: 3},
		{Label: "label-1", TransactionType: pbpartner.SparkTransactionType_SPARK_TRANSACTION_TYPE_TRANSFER, VolumeSats: 87000, TransactionCount: 17},
		{Label: "label-2", TransactionType: pbpartner.SparkTransactionType_SPARK_TRANSACTION_TYPE_TRANSFER, VolumeSats: 99999, TransactionCount: 1},
	}
	require.Len(t, resp.GetEntries(), len(expectedEntries))
	for i, w := range expectedEntries {
		entry := resp.GetEntries()[i]
		assert.Equal(t, w.GetLabel(), entry.GetLabel())
		assert.Equal(t, w.GetTransactionType(), entry.GetTransactionType())
		assert.Equal(t, w.GetVolumeSats(), entry.GetVolumeSats())
		assert.Equal(t, w.GetTransactionCount(), entry.GetTransactionCount())
	}
}

// TestQuerySparkTransactionVolumes_LabelScoped verifies that a label-scoped
// query returns only that label's entries (one per transaction type).
func TestQuerySparkTransactionVolumes_LabelScoped(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	seedTransactionVolumeMV(t, tc.DatabasePath())

	client := partner.NewRisingWaveClient(tc.DatabasePath())
	require.NotNil(t, client)
	defer func() { _ = client.Close() }()

	h := NewPartnerQueryHandler(client)
	resp, err := h.QuerySparkTransactionVolumes(partnerCtx(ctx, "partner-a"), &pbpartner.QuerySparkTransactionVolumesRequest{
		StartDate: "2025-03-01",
		EndDate:   "2025-03-31",
		Label:     "label-1",
	})
	require.NoError(t, err)

	assert.Equal(t, int64(107000), resp.GetTotalVolumeSats())
	assert.Equal(t, int64(20), resp.GetTotalTransactionCount())
	require.Len(t, resp.GetEntries(), 2)
	for _, e := range resp.GetEntries() {
		assert.Equal(t, "label-1", e.GetLabel())
	}
}

// TestQuerySparkTransactionVolumes_EmptyResult verifies that a query matching no
// rows yields no entries and zero grand totals rather than an error.
func TestQuerySparkTransactionVolumes_EmptyResult(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	seedTransactionVolumeMV(t, tc.DatabasePath())

	client := partner.NewRisingWaveClient(tc.DatabasePath())
	require.NotNil(t, client)
	defer func() { _ = client.Close() }()

	h := NewPartnerQueryHandler(client)
	resp, err := h.QuerySparkTransactionVolumes(partnerCtx(ctx, "partner-a"), &pbpartner.QuerySparkTransactionVolumesRequest{
		StartDate: "2030-01-01",
		EndDate:   "2030-01-31",
	})
	require.NoError(t, err)

	assert.Empty(t, resp.GetEntries())
	assert.Zero(t, resp.GetTotalVolumeSats())
	assert.Zero(t, resp.GetTotalTransactionCount())
}

// TestQuerySparkTransactionVolumes_RequiresPartnerAuth verifies the handler
// rejects requests with no partner identity in context.
func TestQuerySparkTransactionVolumes_RequiresPartnerAuth(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	client := partner.NewRisingWaveClient(tc.DatabasePath())
	require.NotNil(t, client)
	defer func() { _ = client.Close() }()

	h := NewPartnerQueryHandler(client)
	_, err := h.QuerySparkTransactionVolumes(ctx, &pbpartner.QuerySparkTransactionVolumesRequest{
		StartDate: "2025-03-01",
		EndDate:   "2025-03-31",
	})
	require.Error(t, err)
}
