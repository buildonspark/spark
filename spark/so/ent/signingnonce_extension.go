package ent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uuids"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent/signingnonce"
	"github.com/lightsparkdev/spark/so/frost"
	"go.uber.org/zap"
)

var partitionNameRegex = regexp.MustCompile(`^signing_nonces_(\d{8})$`)

const (
	// Maximum number of attempts to check if partition detach has completed
	maxDetachPollAttempts = 30
	// Initial backoff duration between detach completion checks
	initialDetachBackoff = 100 * time.Millisecond
	// Maximum backoff duration between detach completion checks
	maxDetachBackoff = 5 * time.Second
)

// IsSigningNoncesPartitioned checks if the signing_nonces table is partitioned.
// Returns true if partitioned, false if regular table.
// This is useful during migration transition to determine which cleanup task to run.
func IsSigningNoncesPartitioned(ctx context.Context, db rawQueryDB) (bool, error) {
	var isPartitioned bool
	rows, err := db.QueryContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON pt.partrelid = c.oid
			WHERE c.relname = 'signing_nonces'
		)
	`)
	if err != nil {
		return false, fmt.Errorf("failed to check if signing_nonces is partitioned: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	if rows.Next() {
		if err := rows.Scan(&isPartitioned); err != nil {
			return false, fmt.Errorf("failed to scan partition check result: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("error iterating partition check result: %w", err)
	}
	return isPartitioned, nil
}

type rawQueryDB interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type partitionDB interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func isDetachPartitionConcurrentlyUnsupported(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	// PG < 14 reports syntax error for DETACH ... CONCURRENTLY.
	// Some environments may report feature-not-supported.
	return pqErr.Code == "42601" || pqErr.Code == "0A000"
}

// waitForPartitionDetach polls pg_inherits to check if a partition detach has completed.
// Returns true if the partition is no longer attached to the parent table, false if timeout.
func waitForPartitionDetach(ctx context.Context, db partitionDB, parentTable, partitionName string) bool {
	backoffDuration := initialDetachBackoff

	for attempt := range maxDetachPollAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(backoffDuration):
			}
			backoffDuration *= 2 // Exponential backoff
			if backoffDuration > maxDetachBackoff {
				backoffDuration = maxDetachBackoff
			}
		}

		// Check if partition is still attached
		checkQuery := `
			SELECT EXISTS (
				SELECT 1
				FROM pg_inherits
				JOIN pg_class child ON pg_inherits.inhrelid = child.oid
				JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
				WHERE parent.relname = $1
				  AND child.relname = $2
			)`

		rows, err := db.QueryContext(ctx, checkQuery, parentTable, partitionName)
		if err != nil {
			logger := logging.GetLoggerFromContext(ctx)
			logger.With(zap.Error(err)).Sugar().Warnf(
				"failed to check partition detach status for %s (attempt %d), retrying", partitionName, attempt+1)
			continue
		}

		var stillAttached bool
		if rows.Next() {
			if err := rows.Scan(&stillAttached); err != nil {
				_ = rows.Close()
				logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Sugar().Errorf(
					"failed to scan partition detach status for %s (attempt %d)", partitionName, attempt+1)
				return false
			}
		}
		_ = rows.Close()

		if !stillAttached {
			return true
		}
	}

	return false // Timeout
}

// GetSigningNonceFromCommitment returns the signing nonce associated with the given commitment.
func GetSigningNonceFromCommitment(ctx context.Context, _ *so.Config, commitment frost.SigningCommitment) (frost.SigningNonce, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return frost.SigningNonce{}, err
	}

	nonce, err := db.SigningNonce.Query().Where(signingnonce.NonceCommitment(commitment)).First(ctx)
	if err != nil {
		return frost.SigningNonce{}, err
	}

	return nonce.Nonce, nil
}

// GetSigningNoncesForUpdate returns the signing nonces associated with the given commitments, and locks them for update.
func GetSigningNoncesForUpdate(ctx context.Context, _ *so.Config, commitments []frost.SigningCommitment) (map[frost.SigningCommitment]*SigningNonce, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	noncesResult, err := db.SigningNonce.Query().Where(signingnonce.NonceCommitmentIn(commitments...)).ForUpdate().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[frost.SigningCommitment]*SigningNonce, len(noncesResult))
	for _, nonce := range noncesResult {
		result[nonce.NonceCommitment] = nonce
	}
	return result, nil
}

// BulkUpdateRetryFingerprints updates the retry fingerprints for multiple signing nonces in a single query.
func BulkUpdateRetryFingerprints(ctx context.Context, nonces map[frost.SigningCommitment]*SigningNonce, retryFingerprints map[frost.SigningCommitment][]byte) error {
	if len(retryFingerprints) == 0 {
		return nil
	}

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	// Collect all updates to batch them and avoid N+1 queries
	builders := make([]*SigningNonceCreate, 0, len(retryFingerprints))
	for commitment, fingerprint := range retryFingerprints {
		nonce, exists := nonces[commitment]
		if !exists {
			return fmt.Errorf("nonce not found for commitment")
		}

		// Build upsert for batch update. Since records always exist (queried above),
		// OnConflict will always UPDATE, never INSERT. We set ID (for matching), required fields, and the fields we want to update.
		builders = append(builders,
			db.SigningNonce.Create().
				SetID(nonce.ID).
				SetNonce(nonce.Nonce).
				SetNonceCommitment(nonce.NonceCommitment).
				SetRetryFingerprint(fingerprint),
		)
	}

	// Execute all updates in batch to avoid N+1 queries.
	// We use CreateBulk with OnConflict as a workaround since Ent doesn't have native bulk UPDATE support.
	// Since all records exist (queried above), OnConflict will always UPDATE, never INSERT.
	// Batch in chunks to avoid PostgreSQL parameter limit (65535).
	const maxBatchSize = 1000
	for chunk := range slices.Chunk(builders, maxBatchSize) {
		err = db.SigningNonce.CreateBulk(chunk...).
			OnConflictColumns(signingnonce.FieldID).
			Update(func(u *SigningNonceUpsert) {
				u.UpdateRetryFingerprint()
			}).
			Exec(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// Purges old signing_nonce partitions, and creates new ones as needed.
// cutoffTime is exclusive (i.e., will drop partitions with data strictly older than cutoffTime).
// This will create new partitions starting at cutoffTime until maxRequestedPartitionTime, inclusive.
func PurgeAndCreateSigningNoncePartitions(
	ctx context.Context,
	db partitionDB,
	cutoffTime time.Time,
	maxRequestedPartitionTime time.Time,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	cutoffTime = cutoffTime.UTC().Truncate(24 * time.Hour)
	maxRequestedPartitionTime = maxRequestedPartitionTime.UTC().Truncate(24 * time.Hour)

	cutoffDate := cutoffTime.Format("20060102")
	requestedPartitionDates := []string{cutoffDate}
	for t := cutoffTime.Add(24 * time.Hour); !t.After(maxRequestedPartitionTime); t = t.Add(24 * time.Hour) {
		requestedPartitionDates = append(requestedPartitionDates, t.Format("20060102"))
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			child.relname AS partition_name,
			pg_inherits.inhdetachpending AS detach_pending
		FROM
			pg_inherits
		JOIN
			pg_class child ON pg_inherits.inhrelid = child.oid
		JOIN
			pg_class parent ON pg_inherits.inhparent = parent.oid
		WHERE
			parent.relname = 'signing_nonces'`)
	if err != nil {
		return err
	}

	defer func() {
		_ = rows.Close()
	}()

	type partitionInfo struct {
		name          string
		detachPending bool
	}
	var partitionInfos []partitionInfo
	for rows.Next() {
		var partitionName string
		var detachPending bool
		if err := rows.Scan(&partitionName, &detachPending); err != nil {
			return err
		}
		partitionInfos = append(partitionInfos, partitionInfo{name: partitionName, detachPending: detachPending})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Build set of existing partition dates
	existingDates := make(map[string]bool)
	attachedPartitions := make(map[string]bool, len(partitionInfos))
	type stalePartition struct {
		name string
		date string
	}
	var stalePartitionsToDrop []stalePartition

	for _, partitionInfo := range partitionInfos {
		partitionName := partitionInfo.name
		matches := partitionNameRegex.FindStringSubmatch(partitionName)
		if len(matches) != 2 {
			// Only check partitions that match the expected naming pattern signing_nonces_YYYYMMDD
			continue
		}

		attachedPartitions[partitionName] = true
		partitionDate := matches[1]
		existingDates[partitionDate] = true

		// Collect partitions older than cutoff for detach/drop processing.
		if partitionDate < cutoffDate { // Direct string comparison works because of the YYYYMMDD format
			if !partitionInfo.detachPending {
				// Detach partition before dropping to avoid long locks on signing_nonces table
				detachQuery := fmt.Sprintf("ALTER TABLE signing_nonces DETACH PARTITION %s CONCURRENTLY", pq.QuoteIdentifier(partitionName))
				_, err := db.ExecContext(ctx, detachQuery)
				if err != nil {
					if isDetachPartitionConcurrentlyUnsupported(err) {
						logger.With(zap.Error(err)).Sugar().Warnf(
							"DETACH PARTITION CONCURRENTLY unsupported for %s (date %s), retrying without CONCURRENTLY",
							partitionName, partitionDate,
						)
						detachSyncQuery := fmt.Sprintf(
							"ALTER TABLE signing_nonces DETACH PARTITION %s",
							pq.QuoteIdentifier(partitionName),
						)
						if _, err = db.ExecContext(ctx, detachSyncQuery); err != nil {
							logger.With(zap.Error(err)).Sugar().Errorf(
								"failed to detach partition %s for date %s without CONCURRENTLY",
								partitionName, partitionDate,
							)
							continue // Log but don't fail entire operation
						}
					} else {
						logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Sugar().Errorf(
							"failed to detach partition %s for date %s", partitionName, partitionDate)
						continue // Log but don't fail entire operation
					}
				}
			} else {
				logger.Sugar().Infof(
					"detach already pending for partition %s, waiting for completion",
					partitionName,
				)
			}
			stalePartitionsToDrop = append(stalePartitionsToDrop, stalePartition{
				name: partitionName,
				date: partitionDate,
			})
		}
	}

	// Wait for all scheduled detaches, then drop detached partitions.
	for _, stale := range stalePartitionsToDrop {
		if !waitForPartitionDetach(ctx, db, "signing_nonces", stale.name) {
			logging.GetLoggerFromContext(ctx).Sugar().Warnf(
				"partition detach did not complete within timeout for %s, will retry next run", stale.name)
			continue // Skip DROP, try again next time
		}

		dropQuery := fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(stale.name))
		_, err = db.ExecContext(ctx, dropQuery)
		if err != nil {
			logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Sugar().Errorf(
				"failed to drop partition %s", stale.name)
			continue // Log but don't fail entire operation
		}

		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"dropped old signing_nonces partition %s for date %s", stale.name, stale.date)
		delete(existingDates, stale.date)
	}

	// Find any orphaned partition-like tables left behind if cleanup was interrupted
	// (e.g., detached successfully but process crashed before DROP).
	orphanRows, err := db.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON c.relnamespace = n.oid
		WHERE c.relkind = 'r'
			AND n.nspname = CURRENT_SCHEMA()
			AND c.relname ~ '^signing_nonces_[0-9]{8}$'`)
	if err != nil {
		return err
	}
	defer func() {
		_ = orphanRows.Close()
	}()

	var partitionLikeTables []string
	for orphanRows.Next() {
		var tableName string
		if err := orphanRows.Scan(&tableName); err != nil {
			return err
		}
		partitionLikeTables = append(partitionLikeTables, tableName)
	}
	if err := orphanRows.Err(); err != nil {
		return err
	}
	orphanPartitionNames := make(map[string]bool)
	for _, tableName := range partitionLikeTables {
		if attachedPartitions[tableName] {
			continue
		}
		matches := partitionNameRegex.FindStringSubmatch(tableName)
		if len(matches) != 2 {
			continue
		}

		partitionDate := matches[1]
		orphanPartitionNames[tableName] = true
		if partitionDate >= cutoffDate {
			logger.Sugar().Warnf(
				"found orphaned signing_nonces table %s for date %s (>= cutoff), skipping auto-drop",
				tableName, partitionDate,
			)
			continue
		}

		dropQuery := fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(tableName))
		if _, err := db.ExecContext(ctx, dropQuery); err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf(
				"failed to drop orphaned signing_nonces table %s for date %s", tableName, partitionDate)
			continue
		}

		logger.Sugar().Infof(
			"dropped orphaned signing_nonces table %s for date %s", tableName, partitionDate,
		)
		delete(orphanPartitionNames, tableName)
	}

	for _, dateStr := range requestedPartitionDates {
		if existingDates[dateStr] {
			continue // Partition already exists
		}

		// Parse date to create FROM/TO UUIDs
		t, err := time.Parse("20060102", dateStr)
		if err != nil {
			logging.GetLoggerFromContext(ctx).Error(
				"failed to parse partition date",
				zap.String("date", dateStr),
				zap.Error(err),
			)
			continue
		}

		partitionName := fmt.Sprintf("signing_nonces_%s", dateStr)
		fromUUID, toUUID := uuids.UUIDRangeForDate(t)

		// If a table with this name already exists and is not attached, reuse it by ATTACHing.
		// This avoids blocking all subsequent partition creation for this run.
		partitionTableExisted := orphanPartitionNames[partitionName]

		createStandaloneQuery := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s (LIKE signing_nonces INCLUDING CONSTRAINTS INCLUDING DEFAULTS)",
			pq.QuoteIdentifier(partitionName),
		)
		if _, err = db.ExecContext(ctx, createStandaloneQuery); err != nil {
			logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Sugar().Errorf(
				"failed to create standalone partition table %s for date %s",
				partitionName, dateStr,
			)
			continue // Log but don't fail entire operation
		}

		// ATTACH PARTITION does not support CONCURRENTLY. These partitions are
		// future partitions and expected to be empty, so validation is fast.
		attachQuery := fmt.Sprintf(
			"ALTER TABLE signing_nonces ATTACH PARTITION %s FOR VALUES FROM ('%s') TO ('%s')",
			pq.QuoteIdentifier(partitionName), fromUUID.String(), toUUID.String(),
		)
		if _, err = db.ExecContext(ctx, attachQuery); err != nil {
			logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Sugar().Errorf(
				"failed to attach partition %s for date %s (FROM '%s' TO '%s')",
				partitionName, dateStr, fromUUID.String(), toUUID.String(),
			)
			// Best-effort cleanup only for tables created in this run, to avoid creating
			// a new orphan that blocks future partition management.
			if !partitionTableExisted {
				dropQuery := fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(partitionName))
				if _, dropErr := db.ExecContext(ctx, dropQuery); dropErr != nil {
					logging.GetLoggerFromContext(ctx).With(zap.Error(dropErr)).Sugar().Warnf(
						"failed to clean up unattached partition table %s for date %s",
						partitionName, dateStr,
					)
				}
			}
			continue // Log but don't fail entire operation
		}

		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"created and attached signing_nonces partition %s for date %s (FROM '%s' TO '%s')",
			partitionName, dateStr, fromUUID.String(), toUUID.String(),
		)
	}

	return nil
}
