# dbseed — context for Claude

Utility for populating `transfers`, `transfer_senders`, `transfer_receivers`
with prod-shaped synthetic data, so the Postgres planner picks realistic
plans against a local SO database. Useful any time you need tens of millions
of transfer rows for query-planner work without the ceremony of real wallets.

See `README.md` for how to run it. This file is the *why* — read before
extending the tool or debugging unexpected output.

## Design rationale (non-obvious choices)

### Bypasses Ent entirely

The three target tables have Ent hooks that would make bulk generation
impractical:

- `transfer_leaves` (which we *don't* touch) has create-hooks that parse
  pre-signed Bitcoin transactions out of `raw_refund_tx` bytes and derive
  txids + timelocks. Going through Ent for transfers drags this in
  transitively via eager-loads.
- `keys.Public` validates that input bytes are a valid compressed EC point.
  Our synthetic pubkeys aren't real EC points — they're sha256-derived
  33-byte blobs shaped like `0x02` or `0x03` prefix + 32-byte payload. The
  database doesn't care (columns are `bytea`), but Ent would reject them.

So we use `pgx.CopyFrom` directly against the three tables. This has two
further consequences: (a) we must write the columns in the exact order the
database expects them, which is documented in the `CopyFrom` call sites in
`seed.go`; (b) any required NOT NULL column with no default *must* be seeded
or COPY fails — if you add a new NOT NULL field to the schema, you'll need
to handle it here.

### Imports `schematype` enums directly

`generator.go` and `config.go` import `st "github.com/lightsparkdev/spark/so/ent/schema/schematype"`
and reference status/type constants by their Go identifier, not by string
literal. This is deliberate — a schema edit that renames
`TransferStatusSenderKeyTweaked` will break this package's build. With string
literals the tool would silently seed `SENDER_KEY_TWEAKED` rows that no
longer exist in the enum, and `EXPLAIN` would diverge from prod without a
clear signal.

**If you're touching enums:** update the schematype file first, regen Ent,
then the dbseed build will tell you where to update status weights.

### Reads `indexdef` at drop time, doesn't hardcode indexes

`seed.go:snapshotAndDropIndexes` queries `pg_indexes.indexdef` for every
non-PK index on the three tables, drops them, then replays the same strings
verbatim in `rebuildIndexes`. `seed.go:snapshotAndDropForeignKeys` does the
same for FK constraints via `pg_get_constraintdef`. This means:

- The tool stays correct automatically as new indexes or FKs are added —
  dbseed picks them up with no code changes.
- You never have to keep a list of indexes in sync with the schema here.

**Gotcha:** if Postgres ever changes how it pretty-prints index or constraint
definitions between major versions, the replay could fail. Not observed in
PG 14-17; if it happens, consider parsing and re-emitting instead.

### `-recover-schema` reads from Ent's `migrate.Tables`, doesn't hardcode either

`main.go:recoverEntSchema` uses Ent's own `schema.NewMigrate` scoped to
`migrate.TransfersTable` + `migrate.TransferSendersTable` +
`migrate.TransferReceiversTable` + the two FK-referenced tables
(`PaymentIntentsTable`, `SparkInvoicesTable`). The index and FK definitions
come straight from `so/ent/migrate/schema.go`, which is regenerated every
time `mise gen-ent` runs. Any schema addition is picked up automatically.

**Why not unscoped `client.Schema.Create(ctx)`?** It pulls in every table
in the schema and errors on `l1token_justice_transactions` (Ent's
canonical constraint name differs from Atlas's truncated one). Scoping
sidesteps that by touching only tables whose names we've verified are
consistent across Ent and Atlas.

**Why `PaymentIntentsTable` and `SparkInvoicesTable`?** `transfers` has
nullable FKs to these two tables. Ent's `Create` validates that every FK's
ref-table is in the passed list — without them, it errors with
"unexpected fk ref-table". They're included for validation only; Create is
additive, so tables already present no-op.

### Single-producer, three-consumer pipeline

Rows are generated wallet-by-wallet on a single goroutine and fanned out to
three buffered channels, each drained by a separate `pgx.CopyFrom`. The
channels are 10k-buffered; the slowest COPY stalls the producer via
backpressure.

Earlier draft had wallets running concurrently, but that complicates
determinism (output order depends on scheduling) and adds no real speedup
because the bottleneck is the COPY protocol, not row generation. Current
design is the right tradeoff — keep it.

### Schema coupling check

The `id` column on all three tables is `uuid` — we generate UUIDv7 for
temporal ordering alignment with `create_time`. If the schema ever moves to
a different ID strategy (e.g., bigint sequences), this breaks silently at
the pgx type-encoder level. Smoke test after schema changes.

## Things to watch

### After any schema change
- Run `mise gen-ent` in spark/spark/ first. `migrate.TransfersTable` and
  friends regenerate from the schema definitions.
- Build this package (`go build ./cmd/dbseed/...`) — it'll fail if enums
  were renamed.
- Run `-profile=smoke -truncate` against a fresh DB to verify COPY still
  accepts all columns (catches new NOT NULL additions that would break COPY).

### After index changes
- No action required — `indexdef` is read at runtime during the normal seed
  pipeline, and `-recover-schema` reads from Ent's regenerated table vars.
- Verify new index got populated by the rebuild phase's log lines on the
  next seed run.

### If a new FK is added to one of the three tables
- The existing `snapshotAndDropForeignKeys` and `rebuildForeignKeys` already
  handle it automatically (reads from `pg_constraint` at runtime).
- But if the new FK references a table not yet in `recoverEntSchema`'s
  `tables` slice, `-recover-schema` will error with "unexpected fk
  ref-table". Add the referenced table var to that slice — comment in
  `main.go` explains why.

### If COPY throughput drops
Common causes:
- Someone added a trigger to one of the three tables (triggers fire per row
  during COPY, which destroys throughput). Check `pg_trigger` on the three
  tables.
- The PVC is on network-backed storage rather than local SSD. Minikube's
  default `hostpath` provisioner uses node-local disk; check `kubectl get pv`.
- `maintenance_work_mem` not actually 2GB — the `SET` in `rebuildIndexes`
  is session-scoped and only applies on a single acquired connection. If
  someone refactors to use the pool, the setting vanishes on each new conn.

### If partial index cardinality looks wrong
The `full` profile targets ~0.3% `SENDER_KEY_TWEAKED` to populate the
receiver-union partial index. On 11M transfers, expect ~33k rows. If you
see orders of magnitude off, check `config.go:fullConfig` status weights —
they're denominated in an unnormalized weight space where total sums to
10000 (so weight 30 → 0.3%).

### If dual-role dedup isn't triggering
`DualRoleTransfers = 2000` in full profile (20 in smoke). These are
additional transfers whose sender and receiver pubkeys are identical,
using the `DualRoleTierLabel` wallet (currently T4). The anti-join dedup
in queries that UNION sender and receiver arms should exclude these from
double-counting. Verify with:

```sql
SELECT COUNT(*) FROM transfer_senders s
JOIN transfer_receivers r
  ON r.transfer_id = s.transfer_id
  AND r.identity_pubkey = s.identity_pubkey;
-- Expected: ~2000 on full profile; ~20 on smoke.
```

If this returns 0, check that `DualRoleTierLabel` in `config.go` still
points at an existing tier — a rename without updating this will silently
skip the dual-role pass.

## Not worth doing unless you have a reason

- **Parallelizing wallet generation** — bottleneck is COPY protocol, not gen.
- **Adding more tables** — the query-planner workloads this tool targets don't
  read them. If you genuinely need `transfer_leaves` populated for some other
  workload, that's a separate tool; real Bitcoin tx construction is non-trivial.
- **Supporting non-MAINNET network labels** — the whole point is planner
  fidelity with prod, which is mainnet. If your workload genuinely needs
  regtest-labeled data, parameterize Network and accept that EXPLAIN output
  diverges from prod.
- **Making it an Ent-based seed** — would take 50× longer and solve nothing.
  See "Bypasses Ent entirely" above.
