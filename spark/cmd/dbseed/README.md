# dbseed

Populates an SO Postgres database with prod-shaped synthetic transfer data so
the Postgres planner makes realistic choices on local minikube or a dev
workstation. Profiles range from ~10k rows (iteration) to ~61M (prod SSP
scale), each modeling a different production traffic shape worth validating
queries against.

Only three tables are touched: `transfers`, `transfer_senders`,
`transfer_receivers`. Everything else in the schema (transfer_leaves with
pre-signed Bitcoin transactions, tree_nodes, spark_invoices, signing state,
tokens) is left alone — the transfer-lookup queries this tool targets never
read those tables, and seeding them would require cooking valid FROST
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
| `mise run dbseed-full-no-ssp` | Same ladder minus T1 SSP, ~11.5M rows, 2-4 min |
| `mise run dbseed-realistic-ssp` | Real SSP shape on mainnet+regtest, ~24.5M rows |
| `mise run dbseed-stuck-user` | Stuck-user backlog shape, ~120k rows, seconds |
| `mise run dbseed-recover` | Replay missing indexes/FKs if a run crashed |
| `mise run dbseed-snapshot` | `pg_dump` to `cmd/dbseed/snapshots/` |
| `mise run dbseed-restore <path>` | `pg_restore` into a fresh DB |
| `mise run dbseed -- -dsn=... -profile=smoke -dry-run` | Arbitrary dbseed flags |

All seed tasks default to `sparkoperator_0` via minikube. Override with
`DBSEED_DSN=` to target a different SO (e.g. `sparkoperator_1`):

```bash
DBSEED_DSN="postgres://postgres:postgres@$(minikube ip):5432/sparkoperator_1?sslmode=disable" \
  mise run dbseed-realistic-ssp
```

`dbseed-snapshot` / `dbseed-restore` use `DBSEED_DB=` instead (DB name only,
since they shell out to `kubectl exec` rather than a TCP DSN).

## Flags

| Flag | Default | Notes |
|---|---|---|
| `-dsn` | (required) | Standard lib/pq DSN. The DB must already have migrations applied. |
| `-profile` | `full` | One of: `full`, `full-no-ssp`, `smoke`, `realistic_ssp`, `stuck_user`. See [Profiles](#profiles) for shapes and use cases. |
| `-truncate` | `false` | `TRUNCATE transfers, transfer_senders, transfer_receivers CASCADE` before seeding. Skip if you want to stack on top of existing data (not recommended — risks pubkey collisions). |
| `-seed` | `1` | Random seed. Deterministic: same `-seed` + same profile produces byte-identical rows. Use different values if you need multiple distinct datasets. |
| `-dry-run` | `false` | Print the distribution plan and exit. Doesn't touch the DB. |

## Profiles

Five profiles, two generation models. Pick one per use case:

| Profile | Use when… | Total rows | Time | Network | Driver |
|---|---|---|---|---|---|
| `full` | Default — exercises every wallet-size tier including the 25M-edge SSP wallet. Right for any query-planner work where you don't have a more specific shape in mind. | ~61M | 15-25 min | MAINNET | Tiers |
| `full-no-ssp` | Same as `full` minus the T1 SSP wallet — fast iteration when you need the prod-shaped ladder but can't pay the 25M-edge cost. | ~11.5M | 2-4 min | MAINNET | Tiers |
| `smoke` | Iterating on the generator itself or the snapshot/restore workflow — *not* for query work. | ~10k | seconds | MAINNET | Tiers |
| `realistic_ssp` | Validating queries against the *real* SSP cardinality shape — small pending count behind a multi-million completed backdrop, on both networks. Use when SSP-specific plan choice matters and the synthetic T1 wallet's distribution would mislead. | ~24.5M | 15-25 min | MAINNET + REGTEST | WalletGroups |
| `stuck_user` | Validating queries against the worst-case unclaimed-inbound-backlog shape — pubkey with tens of thousands of pending TRANSFERs, almost all INITIATED. Use when the question is "does this hold up at the high end of pending-receiver cardinality, where SSP shape doesn't reach". | ~120k | seconds | MAINNET | WalletGroups |

**Tiers vs WalletGroups**: `full` / `full-no-ssp` / `smoke` are tier-driven —
a population of synthetic wallets at varying scales, all sampling from the
same global status/type distribution. `realistic_ssp` / `stuck_user` are
WalletGroup-driven — specific concrete pubkeys with exact cardinality and
their own per-phase status/type mix, modeled directly from prod observations.
Both models can stack via `-truncate=false` on a second pass; see CLAUDE.md
for the why.

### `full` — prod-shaped distribution

Use this when you don't have a more specific shape in mind. Default profile.

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

**Network**: every `full` row is labeled `MAINNET` regardless of where you're
running. The partial indexes and production queries all include
`network = 'MAINNET'` as leading equality; using REGTEST for synthetic data
would give you different query plans than prod. (`realistic_ssp` deliberately
breaks this rule for the regtest SSP fixture — see its own section.)

### `full-no-ssp` — same ladder, no T1 SSP

Use this when you want the prod-shaped tier ladder but can't sit through the
~15-25 min the SSP T1 wallet costs. Same status/type/network shape as `full`
— just drops T1 (the 50M-rowCount synthetic SSP). Everything else, including
the dual-role pass, is identical.

| What's kept | What's dropped |
|---|---|
| T2 / T3 / T4 / T5 / TAIL | T1 (1 wallet, 50M rowCount) |
| 2000 dual-role transfers on T4 | — |
| Full status/type distributions | — |

Total: ~11.5M transfers. Generates in 2-4 min. Suitable for iterating on
partial-index behavior, mid-tier wallet shapes, and small/medium query work
where the SSP outlier isn't load-bearing.

### `smoke` — iteration profile

Use this when you're working on the dbseed generator itself, the
snapshot/restore workflow, or anything where ~10k rows is enough — *not* for
real query-planner work (10k rows is too small for partial-index cost
estimates to be meaningful).

Same tier/status/type shape as `full` but ~1000× smaller. ~10k transfers,
finishes in seconds.

### `realistic_ssp` — real SSP traffic shape

Use this when a query's behavior on real-SSP cardinality matters and the
synthetic T1 wallet from `full` would mislead. The T1 wallet has ~25M edges
distributed across the global status mix (~0.3% pending, etc.), which doesn't
match how a real SSP looks: low-hundreds of pending receivers behind ~24M
COMPLETED, with a swap-family-dominated type histogram. Run this profile when
that distinction would change the plan or the cost.

Reproduces a real SSP's *pending* receiver mix on top of a realistic completed
backdrop, on each network. Modeled directly from prod cardinality probes
against `transfer_receivers` (captured 2026-04-28; see `CLAUDE.md` in this dir
for the probe SQL). Designed for `queryPendingTransfersMIMO` validation — the
SSP-side perf question this profile answers.

Two synthetic SSP wallets, each on its own network:

| Wallet | Network | Pending phase | Completed phase | Pending status mix |
|---|---|---|---|---|
| `ssp-mainnet` | MAINNET | 92 receivers | 23,729,623 receivers | 86 INITIATED, 3 REFUND_SIGNED, 2 KEY_TWEAKED, 1 KEY_TWEAK_LOCKED |
| `ssp-regtest` | REGTEST | 64 receivers | 752,154 receivers | 61 INITIATED, 2 REFUND_SIGNED, 1 KEY_TWEAK_APPLIED |

Pending type mix differs sharply between networks: mainnet pending is
PREIMAGE_SWAP-dominated (~53%), regtest pending is SWAP-dominated (~81%) —
both shapes are exercised so plan choice can be evaluated against either.

The completed backdrops matter for plan choice: postgres tracks per-pubkey
row counts via `pg_stats.most_common_vals`, so a synthetic SSP pubkey with
only 92 rows total looks nothing like a prod SSP pubkey with 23.7M rows.
Without the backdrop, the planner under-estimates how much the partial
index saves over walking the full per-pubkey index.

Total transfer rows: ~24.5M. Wall-clock comparable to `full` but skewed
toward index rebuild rather than COPY. Tiers and dual-role transfers from
`full` are NOT included — this profile is entirely WalletGroup-driven.

> **Note**: This profile writes both MAINNET and REGTEST rows. The
> existing `full` profile is mainnet-only by design (planner fidelity with
> prod). Do not stack these profiles on top of each other unless you
> specifically want both shapes in the same dataset.

### `stuck_user` — unclaimed-inbound backlog

Use this when a query's behavior at the *high end* of pending-receiver
cardinality matters — the opposite shape from `realistic_ssp`. Where the SSP
has ~92 pending behind 23.7M completed (pending is a tiny needle), a stuck
user has ~58k pending behind ~61k total (pending IS the haystack). Same
query, same indexes, completely different cost surface — and the 65535
bind-parameter crash zone sits right around 50k-100k pending. This profile
surfaces those failure modes; `realistic_ssp` doesn't.

Reproduces the tail of stuck-user wallets — identities sitting on tens of
thousands of unclaimed inbound TRANSFERs in `INITIATED` status. The primary
fixture matches the worst-case prod pubkey
(`0329dd5999cc2ac895cb24118c0df7009ab4ca659e5d247f1857de91a869069c24`,
~58.9k pending receivers, ~100% INITIATED, ~100% TRANSFER); five smaller
secondary fixtures cover the next-largest stuck users so the planner doesn't
get to optimize against a single outlier and look fine.

| Wallet | Network | Pending phase | Completed phase |
|---|---|---|---|
| `stuck-user-primary` | MAINNET | 58,953 | 2,447 |
| `stuck-user-02c65776` | MAINNET | 27,657 | — |
| `stuck-user-035a3abb` | MAINNET | 14,092 | — |
| `stuck-user-038215a6` | MAINNET | 10,800 | — |
| `stuck-user-0344608d` | MAINNET | 4,563 | — |
| `stuck-user-023efa8b` | MAINNET | 3,274 | — |

Pending status: ≥99.99% `INITIATED`. Pending type: ≥99% `TRANSFER` (a tiny
COUNTER_SWAP / PREIMAGE_SWAP tail mirrors prod's outlier rows so the planner
sees a representative type histogram).

Total transfer rows: ~120k. Generates in seconds. Tiers and dual-role
transfers from `full` are NOT included.

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
