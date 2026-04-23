package partner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TransactionVolumeRow holds a single row from the materialized view query.
type TransactionVolumeRow struct {
	TransactionType  string
	VolumeSats       int64
	TransactionCount int64
}

// RisingWaveClient wraps a lazy sql.DB connection to RisingWave for querying
// partner transaction volume data. The connection is established on first use.
type RisingWaveClient struct {
	dsn  string
	once sync.Once
	db   *sql.DB
	err  error
}

// NewRisingWaveClient creates a client that will connect to RisingWave lazily
// on the first query. Returns nil if dsn is empty.
func NewRisingWaveClient(dsn string) *RisingWaveClient {
	if dsn == "" {
		return nil
	}
	return &RisingWaveClient{dsn: dsn}
}

// connect establishes the database connection pool on first call.
func (c *RisingWaveClient) connect() (*sql.DB, error) {
	c.once.Do(func() {
		db, err := sql.Open("pgx", c.dsn)
		if err != nil {
			c.err = fmt.Errorf("failed to open risingwave connection: %w", err)
			return
		}
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		c.db = db
	})
	return c.db, c.err
}

// Close closes the underlying database connection pool if it was opened.
func (c *RisingWaveClient) Close() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// QueryTransactionVolumes queries the spark_transaction_volume materialized view
// for the given partner, date range, and optional filters.
// label is empty string to aggregate across all labels.
// txType is nil to include all transaction types.
// network is nil to include all networks; otherwise scoped to the single value
// (one of MAINNET, REGTEST, TESTNET, SIGNET).
func (c *RisingWaveClient) QueryTransactionVolumes(
	ctx context.Context,
	partnerID string,
	label string,
	startDate time.Time,
	endDate time.Time,
	txType *string,
	network *string,
) ([]TransactionVolumeRow, error) {
	db, err := c.connect()
	if err != nil {
		return nil, err
	}

	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("partner_id = $%d", argIdx))
	args = append(args, partnerID)
	argIdx++

	conditions = append(conditions, fmt.Sprintf("date >= $%d", argIdx))
	args = append(args, startDate.Format(time.DateOnly))
	argIdx++

	conditions = append(conditions, fmt.Sprintf("date <= $%d", argIdx))
	args = append(args, endDate.Format(time.DateOnly))
	argIdx++

	if label != "" {
		conditions = append(conditions, fmt.Sprintf("label = $%d", argIdx))
		args = append(args, label)
		argIdx++
	}

	if txType != nil {
		conditions = append(conditions, fmt.Sprintf("transaction_type = $%d", argIdx))
		args = append(args, *txType)
		argIdx++
	}

	if network != nil {
		conditions = append(conditions, fmt.Sprintf("network = $%d", argIdx))
		args = append(args, *network)
		argIdx++ //nolint:ineffassign // keep for consistency; prevents future filters from silently reusing this index
	}

	query := fmt.Sprintf(
		`SELECT transaction_type, SUM(volume_sats), SUM(transaction_count)
		FROM spark_transaction_volume
		WHERE %s
		GROUP BY transaction_type`,
		strings.Join(conditions, " AND "),
	)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("risingwave query failed: %w", err)
	}
	defer rows.Close() //nolint:errcheck // rows.Close error is not actionable here

	var results []TransactionVolumeRow
	for rows.Next() {
		var row TransactionVolumeRow
		if err := rows.Scan(&row.TransactionType, &row.VolumeSats, &row.TransactionCount); err != nil {
			return nil, fmt.Errorf("failed to scan risingwave row: %w", err)
		}
		results = append(results, row)
	}
	return results, rows.Err()
}
