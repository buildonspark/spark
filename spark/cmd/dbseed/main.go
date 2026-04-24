// Command dbseed populates an SO Postgres database with prod-shaped synthetic
// transfers data to drive realistic Postgres planner choices for
// transfer-lookup query work on a local minikube or developer workstation.
//
// Only three tables are touched: transfers, transfer_senders, transfer_receivers.
// Everything else (transfer_leaves with pre-signed Bitcoin transactions,
// tree_nodes, spark_invoices, signing state, tokens) is skipped — those tables
// are never read by QueryTransfers, QueryPendingTransfers, or GetStuckTransfers.
//
// Ent is bypassed entirely. Rows go in via COPY FROM STDIN to hit the planner
// with realistic row counts (tens of millions) at ~200k rows/sec instead of
// the <10k rows/sec Ent's create hooks would impose.
//
// Schema coupling is deliberate: this program imports schematype.TransferStatus
// and schematype.TransferType so any future enum rename breaks the build
// instead of silently seeding stale strings.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent/migrate"
	"go.uber.org/zap"
)

func main() {
	var (
		dsn           = flag.String("dsn", "", "Postgres DSN (required). Example: postgres://postgres:postgres@localhost:5432/sparkoperator_0?sslmode=disable")
		profile       = flag.String("profile", "full", "Distribution profile: 'full' (~61M transfers at prod SSP scale, ~15-35 min), 'full-no-ssp' (~11.5M transfers, 2-4 min), or 'smoke' (~10k transfers, seconds)")
		truncate      = flag.Bool("truncate", false, "TRUNCATE transfers/transfer_senders/transfer_receivers before seeding")
		seed          = flag.Int64("seed", 1, "Random seed for deterministic generation")
		dryRun        = flag.Bool("dry-run", false, "Print the plan (row counts, distributions) and exit without touching the DB")
		recoverSchema = flag.Bool("recover-schema", false, "Only run idempotent Ent schema migration (adds missing indexes/constraints) then exit. Useful if a previous dbseed run failed partway and left indexes dropped.")
	)
	flag.Parse()

	// Build a console-friendly zap logger and inject into the context. Matches
	// the project logging convention (see bin/operator/main.go) — depguard
	// forbids the stdlib `log` package.
	zapLogger, err := zap.NewDevelopment()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = zapLogger.Sync() }()
	log := zapLogger.Sugar()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "error: -dsn is required")
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx = logging.Inject(ctx, zapLogger)

	if *recoverSchema {
		if err := recoverEntSchema(ctx, *dsn); err != nil {
			log.Fatalf("schema recovery failed: %v", err)
		}
		return
	}

	cfg, err := profileConfig(*profile)
	if err != nil {
		log.Fatalf("profile config: %v", err)
	}
	cfg.Seed = *seed

	printPlan(os.Stderr, cfg)
	if *dryRun {
		return
	}

	start := time.Now()
	if err := Seed(ctx, *dsn, cfg, *truncate); err != nil {
		log.Fatalf("seed failed: %v", err)
	}
	log.Infof("done in %s", time.Since(start).Round(time.Second))
}

// recoverEntSchema applies Ent's schema migration idempotently, scoped to JUST
// the three tables dbseed touches (transfers, transfer_senders,
// transfer_receivers). Missing indexes and constraints are added from the
// current Ent schema definition; existing ones are left alone. Drop options
// are off (default) so nothing is ever removed.
//
// Used when a previous dbseed run failed partway through and left the DB
// missing indexes it had dropped in preparation for COPY.
//
// Why scoped to three tables: running unscoped auto-migrate against the full
// schema collides on naming conventions with the historical Atlas migrations
// on unrelated tables (e.g. l1token_justice_transactions has a constraint
// whose name is truncated differently between Ent's naming and Atlas's).
// Scoping to the three tables dbseed manages sidesteps that entirely — we
// don't care about the rest of the schema here.
//
// Because this reads directly from migrate.TransfersTable / etc., any future
// index added to the schema.go definitions is automatically covered: no list
// to keep in sync, no SQL file to edit.
func recoverEntSchema(ctx context.Context, dsn string) error {
	log := logging.GetLoggerFromContext(ctx).Sugar()
	log.Infof("opening DB for scoped schema recovery")
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	drv := entsql.OpenDB(dialect.Postgres, db)

	// Scope to the three tables dbseed manages, plus the tables they reference
	// via FK (payment_intents, spark_invoices, and transfers itself for its
	// self-ref). Ent's Create validates that every FK's ref-table appears in
	// the passed list — otherwise it errors with "unexpected fk ref-table".
	//
	// The referenced tables are included for *validation only*. Ent's Create
	// is additive, so if those tables already exist with all their indexes
	// and columns in place (as they will on any live DB), the migration is a
	// no-op for them. Only missing structure on our three primary tables gets
	// added.
	tables := []*schema.Table{
		migrate.TransfersTable,
		migrate.TransferSendersTable,
		migrate.TransferReceiversTable,
		migrate.PaymentIntentsTable,
		migrate.SparkInvoicesTable,
	}

	log.Infof("applying idempotent schema migration to %d tables (adds missing indexes/FKs, drops nothing)", len(tables))
	m, err := schema.NewMigrate(drv)
	if err != nil {
		return fmt.Errorf("new migrate: %w", err)
	}
	if err := m.Create(ctx, tables...); err != nil {
		return fmt.Errorf("schema create: %w", err)
	}
	log.Infof("schema recovery complete")
	return nil
}
