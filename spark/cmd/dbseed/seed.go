package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lightsparkdev/spark/common/logging"
)

// walletGroupBaseIdx is the globalIdx offset where WalletGroup pubkeys start.
// Group pubkeys live in this high-numbered range so they don't collide with
// tier or long-tail counter-party pubkeys: tier wallets start at 1; the
// long-tail counter-party pool inside counterpartyPubkey lives at
// 100_000..109_999. 1_000_000 leaves plenty of headroom above that pool.
//
// Used by the WalletGroup pass below and by the test harness in
// profiles_test.go — keep both paths in sync via this single declaration.
const walletGroupBaseIdx = 1_000_000

// Seed runs the full orchestration: index snapshot/drop, COPY rows, index
// recreate. Idempotent on re-run if -truncate is passed.
func Seed(ctx context.Context, dsn string, cfg *Config, truncate bool) error {
	log := logging.GetLoggerFromContext(ctx).Sugar()
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	poolCfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	if err := verifySchema(ctx, pool); err != nil {
		return err
	}

	if truncate {
		log.Infof("truncating transfers, transfer_senders, transfer_receivers")
		if _, err := pool.Exec(ctx,
			`TRUNCATE transfers, transfer_senders, transfer_receivers CASCADE`); err != nil {
			return fmt.Errorf("truncate: %w", err)
		}
	}

	log.Infof("snapshotting and dropping non-PK indexes (COPY is ~10x faster without them)")
	indexes, err := snapshotAndDropIndexes(ctx, pool)
	if err != nil {
		return fmt.Errorf("snapshot/drop indexes: %w", err)
	}
	log.Infof("dropped %d non-PK indexes", len(indexes))

	// FK constraints on the three tables must be dropped for two reasons:
	// (1) parallel COPY streams commit independently, so transfer_receivers may
	// try to reference a transfers row not yet visible to the receivers'
	// connection; (2) per-row FK checks during COPY dominate throughput even
	// when all refs are valid. We recreate them after COPY.
	log.Infof("snapshotting and dropping FK constraints on the three tables")
	fks, err := snapshotAndDropForeignKeys(ctx, pool)
	if err != nil {
		return fmt.Errorf("snapshot/drop FKs: %w", err)
	}
	log.Infof("dropped %d FK constraints", len(fks))

	start := time.Now()
	if err := copyRows(ctx, pool, cfg); err != nil {
		return fmt.Errorf("copy rows: %w", err)
	}
	log.Infof("COPY complete in %s", time.Since(start).Round(time.Second))

	log.Infof("tuning maintenance_work_mem and rebuilding indexes")
	if err := rebuildIndexes(ctx, pool, indexes); err != nil {
		return fmt.Errorf("rebuild indexes: %w", err)
	}

	log.Infof("recreating FK constraints")
	if err := rebuildForeignKeys(ctx, pool, fks); err != nil {
		return fmt.Errorf("rebuild FKs: %w", err)
	}

	log.Infof("running ANALYZE on the three tables")
	for _, t := range []string{"transfers", "transfer_senders", "transfer_receivers"} {
		if _, err := pool.Exec(ctx, "ANALYZE "+t); err != nil {
			return fmt.Errorf("analyze %s: %w", t, err)
		}
	}
	return nil
}

func verifySchema(ctx context.Context, pool *pgxpool.Pool) error {
	for _, t := range []string{"transfers", "transfer_senders", "transfer_receivers"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, t,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check %s: %w", t, err)
		}
		if !exists {
			return fmt.Errorf("table %q does not exist — run migrations first", t)
		}
	}
	return nil
}

type indexSnapshot struct {
	name, table, def string
}

func snapshotAndDropIndexes(ctx context.Context, pool *pgxpool.Pool) ([]indexSnapshot, error) {
	rows, err := pool.Query(ctx, `
		SELECT i.indexname, i.tablename, i.indexdef
		FROM pg_indexes i
		JOIN pg_class c ON c.relname = i.indexname
		JOIN pg_index x ON x.indexrelid = c.oid
		WHERE i.tablename IN ('transfers', 'transfer_senders', 'transfer_receivers')
		  AND x.indisprimary = false
		ORDER BY i.tablename, i.indexname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []indexSnapshot
	for rows.Next() {
		var i indexSnapshot
		if err := rows.Scan(&i.name, &i.table, &i.def); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, i := range out {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP INDEX IF EXISTS %q`, i.name)); err != nil {
			return nil, fmt.Errorf("drop %s: %w", i.name, err)
		}
	}
	return out, nil
}

type fkSnapshot struct {
	name, table, def string
}

func snapshotAndDropForeignKeys(ctx context.Context, pool *pgxpool.Pool) ([]fkSnapshot, error) {
	rows, err := pool.Query(ctx, `
		SELECT conname, conrelid::regclass::text, pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid IN (
			'transfers'::regclass,
			'transfer_senders'::regclass,
			'transfer_receivers'::regclass
		) AND contype = 'f'
		ORDER BY conrelid::regclass::text, conname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fkSnapshot
	for rows.Next() {
		var f fkSnapshot
		if err := rows.Scan(&f.name, &f.table, &f.def); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, f := range out {
		if _, err := pool.Exec(ctx,
			fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT IF EXISTS %q`, f.table, f.name)); err != nil {
			return nil, fmt.Errorf("drop fk %s: %w", f.name, err)
		}
	}
	return out, nil
}

func rebuildForeignKeys(ctx context.Context, pool *pgxpool.Pool, fks []fkSnapshot) error {
	log := logging.GetLoggerFromContext(ctx).Sugar()
	// NOT VALID then VALIDATE CONSTRAINT skips the full-table scan that ADD
	// CONSTRAINT normally requires. Our generator keeps referential integrity
	// by construction, so VALIDATE is a formality that's still cheap.
	for i, f := range fks {
		start := time.Now()
		stmt := fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %q %s NOT VALID`, f.table, f.name, f.def)
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("add fk %s: %w", f.name, err)
		}
		if _, err := pool.Exec(ctx,
			fmt.Sprintf(`ALTER TABLE %s VALIDATE CONSTRAINT %q`, f.table, f.name)); err != nil {
			return fmt.Errorf("validate fk %s: %w", f.name, err)
		}
		log.Infof("[%d/%d] recreated FK %s.%s in %s",
			i+1, len(fks), f.table, f.name, time.Since(start).Round(time.Second))
	}
	return nil
}

func rebuildIndexes(ctx context.Context, pool *pgxpool.Pool, indexes []indexSnapshot) error {
	log := logging.GetLoggerFromContext(ctx).Sugar()
	// Session-level tuning for the rebuild. These settings don't persist across
	// connections — the pgxpool may hand us different conns for each Exec — so
	// we acquire a dedicated conn for the session.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	for _, stmt := range []string{
		`SET maintenance_work_mem = '2GB'`,
		`SET max_parallel_maintenance_workers = 4`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("session tune (%q): %w", stmt, err)
		}
	}
	for i, idx := range indexes {
		start := time.Now()
		// indexdef from pg_indexes is the full CREATE INDEX statement; safe to
		// execute verbatim.
		if _, err := conn.Exec(ctx, idx.def); err != nil {
			return fmt.Errorf("recreate %s: %w", idx.name, err)
		}
		log.Infof("[%d/%d] rebuilt %s.%s in %s",
			i+1, len(indexes), idx.table, idx.name, time.Since(start).Round(time.Second))
	}
	return nil
}

// copyRows runs three COPY streams in parallel, one per table. Each wallet's
// generator feeds all three streams; buffered channels keep the pipeline flowing.
//
// Cancellation contract: if any COPY goroutine errors, it records the error
// and cancels a derived context. The producer goroutine selects on that
// context on every channel send, so a single COPY failure unblocks the
// producer instead of deadlocking it on a drained channel. The progress
// goroutine never touches errCh — it only stops on ctx.Done() — so it can't
// silently swallow an error before the drain loop sees it.
func copyRows(ctx context.Context, pool *pgxpool.Pool, cfg *Config) error {
	log := logging.GetLoggerFromContext(ctx).Sugar()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	const buf = 10_000
	transferCh := make(chan transferRow, buf)
	senderCh := make(chan senderRow, buf)
	receiverCh := make(chan receiverRow, buf)

	var transferCount, senderCount, receiverCount atomic.Int64

	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	// Helper: each COPY goroutine records its error and cancels the context
	// so the producer unblocks instead of deadlocking on the drained channel.
	startCopy := func(name string, fn func() error) {
		defer wg.Done()
		if err := fn(); err != nil {
			errCh <- fmt.Errorf("copy %s: %w", name, err)
			cancel()
		}
	}

	wg.Add(3)
	go startCopy("transfers", func() error {
		_, err := copyTransfers(ctx, pool, transferCh, &transferCount)
		return err
	})
	go startCopy("senders", func() error {
		_, err := copySenders(ctx, pool, senderCh, &senderCount)
		return err
	})
	go startCopy("receivers", func() error {
		_, err := copyReceivers(ctx, pool, receiverCh, &receiverCount)
		return err
	})

	// Progress logger. Listens only on ctx.Done() — never errCh — so error
	// values stay in the buffer for the post-close drain loop.
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				log.Infof("  progress: transfers=%d senders=%d receivers=%d",
					transferCount.Load(), senderCount.Load(), receiverCount.Load())
			case <-ctx.Done():
				return
			}
		}
	}()

	// Producer runs in its own goroutine so the main copyRows body can
	// orchestrate cleanup regardless of whether the producer finished
	// normally or was unblocked by ctx cancellation.
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		globalIdx := 1
		for _, tier := range cfg.Tiers {
			for walletInTier := 0; walletInTier < tier.WalletsInTier; walletInTier++ {
				w := walletID{tierLabel: tier.Label, tierIdx: walletInTier, globalIdx: globalIdx}
				g := newGenerator(cfg, w, cfg.Seed)
				rowCount := tier.CountMin
				if tier.CountMax > tier.CountMin {
					rowCount = tier.CountMin + int(g.rng.Uint64()%uint64(tier.CountMax-tier.CountMin+1))
				}
				// Each wallet emits rows as BOTH sender and receiver of its
				// own transfers. Half self=sender, half self=receiver.
				half := rowCount / 2
				if err := g.emit(ctx, half, false, transferCh, senderCh, receiverCh); err != nil {
					return
				}
				if err := g.emit(ctx, rowCount-half, true, transferCh, senderCh, receiverCh); err != nil {
					return
				}
				globalIdx++
			}
		}
		// Dual-role pass.
		dualGlobalIdx := firstGlobalIdxForTier(cfg, cfg.DualRoleTierLabel)
		if dualGlobalIdx > 0 && cfg.DualRoleTransfers > 0 {
			w := walletID{tierLabel: cfg.DualRoleTierLabel + "-dual", tierIdx: 0, globalIdx: dualGlobalIdx}
			g := newGenerator(cfg, w, cfg.Seed^0xD)
			_ = emitDualRole(ctx, g, cfg.DualRoleTransfers, transferCh, senderCh, receiverCh)
		}
		// WalletGroup pass — used by realistic_ssp / stuck_user profiles.
		// Group pubkey assignment uses walletGroupBaseIdx (package-level
		// const, see below) so they don't collide with tier or long-tail
		// counter-party pubkeys.
		for groupIdx, group := range cfg.WalletGroups {
			groupGlobalIdx := walletGroupBaseIdx + groupIdx
			for phaseIdx, phase := range group.Phases {
				w := walletID{
					tierLabel: group.Label + "/" + phase.Label,
					tierIdx:   phaseIdx,
					globalIdx: groupGlobalIdx,
				}
				// Per-phase rng seed — different phases of the same group must
				// produce uncorrelated rows so the planner sees independent
				// per-row counterparty / value distributions even though the
				// wallet's identity pubkey is shared.
				g := newGenerator(cfg, w, cfg.Seed^int64(0xA00+phaseIdx))
				if err := g.emitPhase(ctx, phase.Count, phase.Role, group.Network,
					newPhaseCDFs(phase),
					transferCh, senderCh, receiverCh); err != nil {
					return
				}
			}
		}
	}()

	<-producerDone
	close(transferCh)
	close(senderCh)
	close(receiverCh)
	wg.Wait()
	cancel() // stop the progress goroutine now that COPYs are done
	<-progressDone
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	log.Infof("final counts: transfers=%d senders=%d receivers=%d",
		transferCount.Load(), senderCount.Load(), receiverCount.Load())
	return nil
}

// firstGlobalIdxForTier returns the globalIdx of the first wallet in the named
// tier, or 0 if not found.
func firstGlobalIdxForTier(cfg *Config, label string) int {
	idx := 1
	for _, t := range cfg.Tiers {
		if t.Label == label {
			return idx
		}
		idx += t.WalletsInTier
	}
	return 0
}

// emitDualRole creates transfers whose sender and receiver pubkeys are identical.
// Anti-join dedup in queries that UNION sender and receiver arms must exclude
// these from double-counting.
// Returns ctx.Err() if the context is canceled mid-emit (e.g. a COPY goroutine
// failed) so the caller can drop out instead of deadlocking on the channel.
func emitDualRole(ctx context.Context, g *generator, count int,
	transferCh chan<- transferRow,
	senderCh chan<- senderRow,
	receiverCh chan<- receiverRow,
) error {
	selfPk := pubkey(g.wallet.globalIdx)
	for range count {
		tid, err := uuid.NewV7()
		if err != nil {
			tid = uuid.New()
		}
		ct := g.randomCreateTime()
		status := g.pickStatus()
		ttype := g.pickType()
		tr := transferRow{
			id:                     tid,
			createTime:             ct,
			updateTime:             ct,
			senderIdentityPubkey:   selfPk,
			receiverIdentityPubkey: selfPk,
			network:                g.cfg.Network,
			totalValue:             int64(1_000 + g.rng.Uint64()%1_000_000),
			status:                 status,
			transferType:           ttype,
			expiryTime:             g.randomExpiry(ct, status),
		}
		sr := senderRow{id: g.newRowID(), createTime: ct, updateTime: ct, transferID: tid, identityPubkey: selfPk, transferType: ttype}
		rr := receiverRow{id: g.newRowID(), createTime: ct, updateTime: ct, transferID: tid, identityPubkey: selfPk, status: receiverStatusForTransfer(status), transferType: ttype}
		select {
		case transferCh <- tr:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case senderCh <- sr:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case receiverCh <- rr:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func copyTransfers(ctx context.Context, pool *pgxpool.Pool, ch <-chan transferRow, counter *atomic.Int64) (int64, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	return conn.Conn().CopyFrom(ctx,
		pgx.Identifier{"transfers"},
		[]string{
			"id", "create_time", "update_time",
			"sender_identity_pubkey", "receiver_identity_pubkey",
			"network", "total_value", "status", "type", "expiry_time",
		},
		pgx.CopyFromFunc(func() ([]any, error) {
			r, ok := <-ch
			if !ok {
				return nil, nil
			}
			counter.Add(1)
			return []any{
				r.id, r.createTime, r.updateTime,
				r.senderIdentityPubkey, r.receiverIdentityPubkey,
				r.network, r.totalValue, string(r.status), string(r.transferType), r.expiryTime,
			}, nil
		}),
	)
}

func copySenders(ctx context.Context, pool *pgxpool.Pool, ch <-chan senderRow, counter *atomic.Int64) (int64, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	return conn.Conn().CopyFrom(ctx,
		pgx.Identifier{"transfer_senders"},
		[]string{"id", "create_time", "update_time", "transfer_id", "identity_pubkey", "transfer_type"},
		pgx.CopyFromFunc(func() ([]any, error) {
			r, ok := <-ch
			if !ok {
				return nil, nil
			}
			counter.Add(1)
			return []any{r.id, r.createTime, r.updateTime, r.transferID, r.identityPubkey, string(r.transferType)}, nil
		}),
	)
}

func copyReceivers(ctx context.Context, pool *pgxpool.Pool, ch <-chan receiverRow, counter *atomic.Int64) (int64, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	return conn.Conn().CopyFrom(ctx,
		pgx.Identifier{"transfer_receivers"},
		[]string{"id", "create_time", "update_time", "transfer_id", "identity_pubkey", "status", "transfer_type"},
		pgx.CopyFromFunc(func() ([]any, error) {
			r, ok := <-ch
			if !ok {
				return nil, nil
			}
			counter.Add(1)
			return []any{r.id, r.createTime, r.updateTime, r.transferID, r.identityPubkey, string(r.status), string(r.transferType)}, nil
		}),
	)
}
