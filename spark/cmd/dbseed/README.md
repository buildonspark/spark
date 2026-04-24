# dbseed

Populates an SO Postgres database with prod-shaped synthetic transfer data so
the Postgres planner makes realistic choices on local minikube or a dev
workstation. Useful any time you need tens of millions of `transfers` /
`transfer_senders` / `transfer_receivers` rows for query-planner work without
the ceremony of real wallets.

Only three tables are touched. Everything else in the schema (transfer_leaves
with pre-signed Bitcoin transactions, tree_nodes, spark_invoices, signing
state, tokens) is left alone — the transfer-lookup queries this tool targets
never read those tables, and seeding them would require cooking valid FROST
signatures and Bitcoin txs.

## Quick start

```bash
# 1. Bring up your local SO Postgres (minikube or whatever your workflow uses)
#    and confirm migrations have applied:
kubectl exec -n default postgres-0 -- psql -U postgres -d sparkoperator_0 \
  -c "SELECT version FROM atlas_schema_revisions.atlas_schema_revisions ORDER BY version DESC LIMIT 1;"

# 2. Smoke test (~10k transfers, ~few seconds)
go run ./cmd/dbseed \
  -dsn="postgres://postgres:postgres@$(minikube ip):5432/sparkoperator_0?sslmode=disable" \
  -profile=smoke \
  -truncate

# 3. Full run (~61M transfers at prod SSP scale, ~40 GB, 15-25 min)
go run ./cmd/dbseed -dsn=... -profile=full -truncate
# Or via mise:
mise run dbseed-full

# 4. Snapshot so you can teardown minikube without losing the seed:
mise run dbseed-snapshot
# Writes to spark/spark/cmd/dbseed/snapshots/ (gitignored)

# 5. Restore later:
mise run dbseed-restore cmd/dbseed/snapshots/sparkoperator_0-YYYYMMDDTHHMMSSZ.pgdump
```

### Via mise (from spark/spark/)

| Task | Purpose |
|---|---|
| `mise run dbseed-smoke` | ~10k rows, seconds — iterate on generator |
| `mise run dbseed-full` | Prod-scale ~61M rows, ~15-25 min |
| `mise run dbseed-recover` | Replay missing indexes/FKs if a run crashed |
| `mise run dbseed-snapshot` | `pg_dump` to `cmd/dbseed/snapshots/` |
| `mise run dbseed-restore <path>` | `pg_restore` into a fresh DB |
| `mise run dbseed -- -dsn=... -profile=smoke -dry-run` | Arbitrary dbseed flags |

All tasks default to `sparkoperator_0` via minikube. Override with
`DBSEED_DSN=` (for dbseed / dbseed-smoke / dbseed-full / dbseed-recover)
or `DBSEED_DB=` (for dbseed-snapshot / dbseed-restore).

## Flags

| Flag | Default | Notes |
|---|---|---|
| `-dsn` | (required) | Standard lib/pq DSN. The DB must already have migrations applied. |
| `-profile` | `full` | `full` = prod-shaped ladder including the 25M-edge SSP wallet (~61M transfers, ~15+ min). `full-no-ssp` = same ladder minus the SSP T1 (~11.5M transfers, ~2-4 min) — fast iteration without the service-wallet cost. `smoke` = ~1000× smaller than `full` for iterating on the generator itself. |
| `-truncate` | `false` | `TRUNCATE transfers, transfer_senders, transfer_receivers CASCADE` before seeding. Skip if you want to stack on top of existing data (not recommended — risks pubkey collisions). |
| `-seed` | `1` | Random seed. Deterministic: same `-seed` + same profile produces byte-identical rows. Use different values if you need multiple distinct datasets. |
| `-dry-run` | `false` | Print the distribution plan and exit. Doesn't touch the DB. |

## Profiles

### `full` — prod-shaped distribution

`rowCount` is total transfer participations. Generator splits 50/50 so
effective per-side count is half of `rowCount`.

| Tier | Wallets | rowCount | Effective /side | Purpose |
|---|---|---|---|---|
| T1 | 1 | 50M | ~25M | SSP-scale mainnet wallet. ~25M edges on each side, reproducing the status-selective query pathology end-to-end at the top of the ladder. |
| T2 | 1 | 10M | ~5M | Large service wallet below SSP scale. Exercises partial-index branches where status-first still walks millions of pending rows. |
| T3 | 1 | 1M | ~500k | Multi-million representative. |
| T4 | 1 | 100k | ~50k | UNION + anti-join stress (symmetric sender+receiver sides). |
| T5 | 3 | 50k-75k | ~25k-37k | The 50k-100k danger zone where legacy MIMO silently truncates and pending branches hit the 65535 bind-parameter crash. |
| TAIL | 1000 | 10-500 | ~5-250 | Long tail so the handler's small-wallet edge-first branch gets exercised. |

Plus ~2000 dual-role transfers (sender pubkey == receiver pubkey on the same
transfer) using the T4 wallet — exercises the anti-join dedup path in
queries that UNION sender and receiver arms.

Total: ~61M transfers, ~122M edge rows. Expect ~40 GB on disk after indexes.
Generation time observed locally: smoke ≈ seconds, full ≈ 15-25 min
(dominated by index rebuild, not COPY).

**Status distribution** (for transfers table):
- ~99.5% `COMPLETED`
- ~0.3% `SENDER_KEY_TWEAKED` (dominates the receiver-union partial index)
- Small minorities of each other pending/stuck status so partial-index
  cardinality is realistic for planner cost estimates.

**Type distribution**: 90% `TRANSFER`, 8% `PREIMAGE_SWAP`, trace `COOPERATIVE_EXIT` / `SWAP`.

**Network**: all rows labeled `MAINNET` regardless of where you're running. The
partial indexes and production queries all include `network = 'MAINNET'` as
leading equality; using REGTEST for synthetic data would give you different
query plans than prod.

### `smoke` — iteration profile

Same tier/status/type shape as `full` but ~1000× smaller. ~10k transfers,
finishes in seconds. Use this when iterating on the generator itself or the
snapshot workflow.

## Outputs

After a run, `ANALYZE` runs on the three tables so planner stats reflect the
new data. You can sanity-check the result with:

```sql
-- Wallet-size distribution by role
SELECT 'receiver' AS role, COUNT(*)
FROM transfer_receivers
GROUP BY identity_pubkey
ORDER BY COUNT(*) DESC LIMIT 10;

-- Partial index population — should be a nontrivial minority (~50k on full)
SELECT status, COUNT(*) FROM transfers
WHERE status IN (
  'SENDER_KEY_TWEAKED','RECEIVER_KEY_TWEAKED','RECEIVER_KEY_TWEAK_LOCKED',
  'RECEIVER_KEY_TWEAK_APPLIED','RECEIVER_REFUND_SIGNED'
) GROUP BY status;

-- Dual-role overlap (should be ~DualRoleTransfers)
SELECT COUNT(*)
FROM transfer_senders s
JOIN transfer_receivers r
  ON r.transfer_id = s.transfer_id
  AND r.identity_pubkey = s.identity_pubkey;
```

## What it temporarily mutates

For COPY throughput, dbseed drops and recreates two things during each run:

- **All non-PK indexes on the three tables** (~20 on a stock schema). Without
  this, COPY runs at ~1/10th throughput because Postgres updates each index
  per row.
- **All FK constraints on the three tables** (5 total). Without this, parallel
  COPY streams fail because transfer_receivers tries to reference transfers
  rows not yet visible from its connection (each COPY runs in its own tx).

If dbseed crashes or is killed between drop and rebuild, the DB is left in a
degraded state. Recover with:

```bash
go run ./cmd/dbseed -dsn=... -recover-schema
```

This runs Ent's idempotent schema migration scoped to just the tables dbseed
manages (plus the two tables they reference via FK). It adds any missing
indexes and FK constraints from the current `schema.go` definitions and
touches nothing else. No hardcoded index list to drift — if the schema grows
a new index, recovery picks it up automatically on the next rebuild.

Scoping avoids the "unrelated Atlas naming collision" issue that unscoped
`client.Schema.Create()` would hit across the full schema.

## Snapshot / restore

The generator's first run dominates wall clock because of index builds — the
full profile takes ~30-35 min end-to-end. A `pg_dump -Fc` right after seeding
costs a few minutes and turns every future run into a few-minute `pg_restore`.

```bash
# Snapshot (writes cmd/dbseed/snapshots/<db>-<timestamp>.pgdump, gitignored)
./scripts/snapshot.sh --minikube sparkoperator_0
# or: mise run dbseed-snapshot

# Restore (works against dirty DBs; pg_restore --clean --if-exists is the default).
# Use --drop for a pristine recreate when the DB is corrupted.
./scripts/restore.sh --minikube sparkoperator_0 <snapshot-path>
# or: mise run dbseed-restore <snapshot-path>
```

`--minikube` routes pg_dump/pg_restore through `kubectl exec` into the
`postgres-0` pod. Omit it if you have direct TCP access to Postgres.

### Snapshot size and timing

The full profile's dataset is **not especially compressible** — each pubkey
and UUID is near-random bytes. Rough expectation on a full seed:

| `-Z` level | Snapshot time | File size |
|---|---|---|
| `-Z0` (no compression) | ~3-5 min | ~20-25 GB |
| `-Z1` (fast, **default**) | ~5-10 min | ~15 GB (TBD) |
| `-Z6` | ~35 min (measured) | 13 GB (measured) |
| `-Z9` | ~50+ min | ~12 GB |

`-Z1` is the sweet spot for local dev — marginal file-size wins beyond it cost
disproportionately more CPU time. Override with `snapshot.sh --compress N` if
you need a specific tradeoff.

## How it works (abridged)

1. **Drops all non-PK indexes** on the three tables after snapshotting their
   `CREATE INDEX` statements from `pg_indexes`. COPY is roughly 10× faster
   without live indexes to maintain.
2. **Fans out a single-producer, three-consumer pipeline** — the wallet loop
   emits rows on three buffered channels, each drained by a parallel
   `pgx.CopyFrom` goroutine. The slowest sink gates the producer via buffer
   backpressure.
3. **Rebuilds indexes** with `maintenance_work_mem=2GB` and
   `max_parallel_maintenance_workers=4` bumped on a dedicated session. Each
   index's build time is logged individually.
4. **Runs `ANALYZE`** on all three tables so `EXPLAIN` plans are based on the
   new stats.

For rationale, schema-coupling guarantees, and knobs to tune if something
looks off, see `CLAUDE.md`.
