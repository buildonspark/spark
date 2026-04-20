package partner

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()
	m.Run()
}

// These tests verify the RisingWave client's connection lifecycle behavior
// (lazy connect, DSN validation). RisingWave is external infrastructure,
// so testing the connection boundary directly is appropriate.

func TestNewRisingWaveClient_EmptyDSN_ReturnsNil(t *testing.T) {
	client := NewRisingWaveClient("")
	assert.Nil(t, client)
}

func TestNewRisingWaveClient_ValidDSN_DoesNotConnectEagerly(t *testing.T) {
	// Use a bogus DSN — NewRisingWaveClient should NOT attempt to connect.
	client := NewRisingWaveClient("postgres://nobody@localhost:1/nonexistent")
	require.NotNil(t, client)
	assert.Nil(t, client.db, "connection should not be established until first query")
}

func TestRisingWaveClient_BadDSN_FailsOnFirstQuery(t *testing.T) {
	client := NewRisingWaveClient("postgres://nobody@localhost:1/nonexistent")
	require.NotNil(t, client)

	_, err := client.QueryTransactionVolumes(
		t.Context(), "partner-a", "", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), nil,
	)
	require.Error(t, err)
}

// TestRisingWaveClient_QueryTransactionVolumes_Postgres uses the test Postgres
// as a stand-in for RisingWave (both speak Postgres wire protocol) to verify
// the query logic end-to-end against a real database.
func TestRisingWaveClient_QueryTransactionVolumes_Postgres(t *testing.T) {
	_, tc := db.ConnectToTestPostgres(t)

	// Create the materialized view schema that exists in RisingWave.
	pgDB, err := sql.Open("pgx", tc.DatabasePath())
	require.NoError(t, err)
	defer pgDB.Close()

	_, err = pgDB.ExecContext(t.Context(), `
		CREATE TABLE IF NOT EXISTS spark_transaction_volume (
			partner_id TEXT NOT NULL,
			label TEXT NOT NULL,
			date TEXT NOT NULL,
			transaction_type TEXT NOT NULL,
			volume_sats BIGINT NOT NULL,
			transaction_count BIGINT NOT NULL
		)
	`)
	require.NoError(t, err)

	// Insert test data.
	_, err = pgDB.ExecContext(t.Context(), `
		INSERT INTO spark_transaction_volume (partner_id, label, date, transaction_type, volume_sats, transaction_count) VALUES
			('partner-a', 'label-1', '2025-03-01', 'TRANSFER', 50000, 10),
			('partner-a', 'label-1', '2025-03-02', 'TRANSFER', 30000, 5),
			('partner-a', 'label-1', '2025-03-01', 'LIGHTNING_SEND', 20000, 3),
			('partner-a', 'label-2', '2025-03-01', 'TRANSFER', 99999, 1),
			('partner-b', 'label-1', '2025-03-01', 'TRANSFER', 88888, 1)
	`)
	require.NoError(t, err)

	client := NewRisingWaveClient(tc.DatabasePath())
	require.NotNil(t, client)
	defer func() { _ = client.Close() }()

	// Query with partner + label filter.
	rows, err := client.QueryTransactionVolumes(
		t.Context(), "partner-a", "label-1", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC), nil,
	)
	require.NoError(t, err)

	byType := make(map[string]TransactionVolumeRow)
	for _, r := range rows {
		byType[r.TransactionType] = r
	}

	assert.Equal(t, int64(80000), byType["TRANSFER"].VolumeSats)
	assert.Equal(t, int64(15), byType["TRANSFER"].TransactionCount)
	assert.Equal(t, int64(20000), byType["LIGHTNING_SEND"].VolumeSats)
	assert.Equal(t, int64(3), byType["LIGHTNING_SEND"].TransactionCount)

	// Query with transaction type filter.
	txType := "TRANSFER"
	rows, err = client.QueryTransactionVolumes(
		t.Context(), "partner-a", "label-1", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC), &txType,
	)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "TRANSFER", rows[0].TransactionType)
	assert.Equal(t, int64(80000), rows[0].VolumeSats)

	// Query without label (aggregates across all labels for partner).
	rows, err = client.QueryTransactionVolumes(
		t.Context(), "partner-a", "", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC), nil,
	)
	require.NoError(t, err)

	var totalVolume int64
	for _, r := range rows {
		totalVolume += r.VolumeSats
	}
	assert.Equal(t, int64(199999), totalVolume)

	// Query for different partner returns only their data.
	rows, err = client.QueryTransactionVolumes(
		t.Context(), "partner-b", "", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC), nil,
	)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(88888), rows[0].VolumeSats)
}
