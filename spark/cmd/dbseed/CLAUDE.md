# dbseed — context for Claude

Utility for populating `transfers`, `transfer_senders`, `transfer_receivers`
with prod-shaped synthetic data, so the Postgres planner picks realistic
plans against a local SO database. Useful any time you need tens of millions
of transfer rows for query-planner work without the ceremony of real wallets.

See `README.md` for how to run it. This file is the *why* — read before
extending the tool or debugging unexpected output.

## Profiles at a glance

Five profiles, two generation models. The README has shapes and use cases;
this is the "which one and why" map for someone extending the tool:

| Profile | Driver | Models | Why it exists |
|---|---|---|---|
| `full` | Tiers | A population of synthetic wallets at varying scales sampling a global status/type mix | Prod-shaped baseline — exercises every wallet-size tier and every code path that branches on cardinality. |
| `full-no-ssp` | Tiers | Same as `full` minus the T1 SSP | Lets you keep the rest of the ladder while skipping the 25M-edge SSP that dominates wall-clock. |
| `smoke` | Tiers | `full` scaled down ~1000× | For iterating on the dbseed code itself — *not* a query-perf profile. |
| `realistic_ssp` | WalletGroups | Real SSP: low-hundreds pending + multi-million COMPLETED, both networks | Validates queries on the SSP shape, where the pending subset is a tiny needle behind a giant haystack. T1 in `full` doesn't reproduce this — its pending set is sampled per the global mix, not pinned to ~92. |
| `stuck_user` | WalletGroups | Real stuck users: tens of thousands of pending TRANSFER + RECEIVER_CLAIM_PENDING (awaiting-claim state) | Validates queries on the *opposite* extreme from `realistic_ssp` — pending is most of the rows, partial-index doesn't help, and 50k-100k pending is the bind-parameter crash zone. |

When in doubt about extending: if the new shape is "a population of wallets
varying by size", it's a tier. If it's "this specific prod identity with this
exact cardinality", it's a WalletGroup with phases.

## Cardinality assessment — required before EXPLAIN

**Always assess cardinality first when querying a dbseed-seeded database for
performance work.** Plan choices only make sense in the context of "how big
is the haystack, how small is the needle." Don't read an EXPLAIN tree before
you can predict its rough shape from cardinality alone.

The canonical workflow lives in the **`mimo-query-perf` skill**
(`~/.claude/skills/mimo-query-perf/SKILL.md`) — read that first for the full
methodology. Key rules from the skill:

- **Cadence**: one probe at a time, report SQL + raw result + interpretation,
  wait for ack before the next probe.
- **Cardinality first, EXPLAIN second**: status counts → per-pubkey counts →
  partial-index population → THEN `EXPLAIN (ANALYZE, BUFFERS)`.
- **Cross-check dbseed vs prod**: same probe via
  `~/.claude/plugins/cache/lightspark/ls-claude/0.28.1/skills/read-db/scripts/spark-rds.sh -o 0 -c "<SQL>" prod`
  (read-only, but no read replicas — be deliberate).
- **Validate plan SHAPE on dbseed first**, not absolute timings; prod cost
  falls out from the cardinality ratio.

### Standard probe queries

The probes you run depend on the query path under study. The most common ones:

#### Status counts (selectivity for partial-index work)

```sql
-- Transfers status histogram
SELECT status, count(*) FROM transfers GROUP BY status ORDER BY count(*) DESC;

-- Transfer-receivers status histogram (where the pending partial index lives)
SELECT status, count(*) FROM transfer_receivers GROUP BY status ORDER BY count(*) DESC;
```

#### Per-pubkey cardinality (wallet-tier sanity)

```sql
-- Top-N wallets by receiver count — verifies the seeded ladder shape
SELECT encode(identity_pubkey,'hex') AS pubkey, count(*) AS receiver_count
FROM transfer_receivers
GROUP BY identity_pubkey ORDER BY count(*) DESC LIMIT 10;

-- Specific pubkey cardinality
SELECT count(*) FROM transfer_receivers
WHERE identity_pubkey = decode('<hex>','hex');
```

#### Partial-index population (the queryable haystack)

Two receiver-pending partial indexes coexist — pick the one matching the
query under study. The legacy `idx_transferreceiver_pending_pubkey_time`
covers a 5-status set including `INITIATED`; the new
`idx_transferreceiver_claim_pending_pubkey_time` covers a 5-status set
including `RECEIVER_CLAIM_PENDING` and excluding `INITIATED`.

```sql
-- Legacy pending partial
SELECT count(*) FROM transfer_receivers
WHERE status IN ('INITIATED','RECEIVER_KEY_TWEAKED','RECEIVER_KEY_TWEAK_LOCKED',
                 'RECEIVER_KEY_TWEAK_APPLIED','RECEIVER_REFUND_SIGNED');

-- New pending partial — what queryPendingTransfersMIMO walks
SELECT count(*) FROM transfer_receivers
WHERE status IN ('RECEIVER_CLAIM_PENDING','RECEIVER_KEY_TWEAKED','RECEIVER_KEY_TWEAK_LOCKED',
                 'RECEIVER_KEY_TWEAK_APPLIED','RECEIVER_REFUND_SIGNED');

-- Same, scoped to one pubkey (matches each partial's leading column)
SELECT count(*) FROM transfer_receivers
WHERE identity_pubkey = decode('<hex>','hex')
  AND status IN ('RECEIVER_CLAIM_PENDING','RECEIVER_KEY_TWEAKED','RECEIVER_KEY_TWEAK_LOCKED',
                 'RECEIVER_KEY_TWEAK_APPLIED','RECEIVER_REFUND_SIGNED');
```

#### Per-pubkey × type breakdown (when type-filtered queries are in scope)

```sql
SELECT t.type, r.status, count(*)
FROM transfer_receivers r
INNER JOIN transfers t ON t.id = r.transfer_id
WHERE r.identity_pubkey = decode('<hex>','hex')
  AND r.status IN ('RECEIVER_CLAIM_PENDING','RECEIVER_KEY_TWEAKED','RECEIVER_KEY_TWEAK_LOCKED',
                   'RECEIVER_KEY_TWEAK_APPLIED','RECEIVER_REFUND_SIGNED')
GROUP BY t.type, r.status ORDER BY count(*) DESC;
```

#### EXPLAIN — only after cardinality is in hand

```sql
EXPLAIN (ANALYZE, BUFFERS)
<the-query-under-study>;
```

Add `VERBOSE` only when you need to see column references in node detail.

### dbseed gotchas to call out in interpretation

**Status correlation** — every receiver row's status is derived from the
transfer's status via `receiverStatusForTransfer`. The two columns stay
consistent across all profiles (no independent sampling), so
`EXISTS`-style queries and any predicate that hops the two status columns
get the right selectivity. Profile authors set transfer-status weights;
the receiver mix falls out automatically.

**Composition vs selectivity** — `full` matches prod *selectivity* (~0.3%
in the receiver-pending partial index) but inverts *composition* (mostly
`SENDER_KEY_TWEAKED` in dbseed; mostly `RECEIVER_CLAIM_PENDING` in prod).
Selectivity drives plan choice, but composition can affect index ordering
and predicate push-down — note both numbers when interpreting.

**INITIATED vs RECEIVER_CLAIM_PENDING** — `INITIATED` is the brief
pre-sender-tweak window; `RECEIVER_CLAIM_PENDING` is the post-tweak /
awaiting-claim state. The bulk of receiver pending in prod lives in
`RECEIVER_CLAIM_PENDING`. The `realistic_ssp` and `stuck_user` profiles
weight transfer.status toward `SENDER_KEY_TWEAKED` (and the four post-claim
`RECEIVER_*` statuses) for their pending phase, so their receivers are all
`RECEIVER_CLAIM_PENDING` or further along — no `INITIATED`. The `full`
profile assigns small minority weight to the pre-tweak transfer statuses
(`SENDER_INITIATED`, `SENDER_INITIATED_COORDINATOR`,
`SENDER_KEY_TWEAK_PENDING`, `APPLYING_SENDER_KEY_TWEAK`) so a small
`INITIATED`-receiver tail exists too — the legacy partial gets populated.

**Profile-specific cardinality** — the same probe gives wildly different
answers on `full` vs `realistic_ssp` vs `stuck_user`. State which profile is
seeded before reporting numbers; an "is this fast enough" answer means
nothing without that context.

**Cross-check ratio** — when comparing dbseed to prod, build a ratio
column. A query that's 2× faster on dbseed than prod will likely be slower
on prod by a similar ratio at minimum; a query that's only 1.05× different
between the two means dbseed is a faithful test bed for that path.

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

### Tiers vs WalletGroups

Two coexisting wallet-generation models, used by different profiles:

- **Tiers** (`full`, `full-no-ssp`, `smoke`): rng-sampled per-tier row counts,
  global status/type/receiver-status distributions sampled per row. Each
  tier wallet emits both sender and receiver halves of its transfers (50/50).
  Right when modeling a *population* of wallets at varying scales without
  caring about any particular wallet's exact shape.

- **WalletGroups** (`realistic_ssp`, `stuck_user`): each group is one concrete
  identity pubkey emitting one or more *phases* with exact row counts, fixed
  role (receiver-only or sender-only), and per-phase distributions overriding
  the Config defaults. Right when modeling a *specific* prod wallet whose
  exact cardinality matters for plan choice (e.g. an SSP's 92 pending
  receivers behind a 23.7M completed backdrop, or a stuck user's 58.9k
  inbound backlog).

Both models can coexist in a single Config in principle, but the current
profiles use one or the other exclusively — `full` profiles are
tier-driven, `realistic_ssp` / `stuck_user` are WalletGroup-driven. To
combine shapes, run dbseed twice with `-truncate=false` on the second pass
(once tier-driven for the backdrop, once WalletGroup-driven for the
specific wallet shape on top).

WalletGroup pubkeys are derived from `globalIdx` in a high-numbered range
(`walletGroupBaseIdx = 1_000_000`) so they don't collide with tier or
long-tail counter-party pubkeys (the long-tail counter-party pool inside
`counterpartyPubkey` lives at `100_000..109_999`).

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
The `full` profile targets ~0.3% `SENDER_KEY_TWEAKED` (weight 30 of 10000)
to populate the receiver-union partial index. On 11M transfers, expect
~33k rows. If you see orders of magnitude off, check
`config.go:fullConfig` status weights — they're denominated in an
unnormalized weight space where total sums to 10000.

For the receiver-side, there are two partials:
`idx_transferreceiver_pending_pubkey_time` (legacy, covers `INITIATED` +
4 `RECEIVER_*`) and `idx_transferreceiver_claim_pending_pubkey_time`
(new, covers `RECEIVER_CLAIM_PENDING` + same 4 `RECEIVER_*`). Receiver
status is derived from transfer status, so the partial populations come
from the `TransferStatuses` weights:

- **Legacy partial** = `INITIATED` (= sum of pre-tweak transfer weights:
  `SenderInitiated:2` + `SenderInitiatedCoordinator:1` +
  `SenderKeyTweakPending:3` = 6) + 4 `RECEIVER_*` (= `ReceiverKeyTweaked:5`
  + `ReceiverKeyTweakLocked:3` + `ReceiverKeyTweakApplied:2` +
  `ReceiverRefundSigned:2` = 12) = **18 of 10000 (~0.18%)**.
- **New partial** = `RECEIVER_CLAIM_PENDING` (= `SenderKeyTweaked:30`) +
  same 4 `RECEIVER_*` (12) = **42 of 10000 (~0.42%)**.

So the new partial should be ~2.3× more populated than the legacy on
`full`. If the new partial is empty, check that
`receiverStatusForTransfer` is wired up — every transfer status should
map to a receiver status.

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
- **Adding REGTEST rows to the tier-driven profiles** — the partial indexes
  and prod queries pin `network = 'MAINNET'`, so REGTEST rows in `full` would
  silently make EXPLAIN diverge from prod. The WalletGroup-driven profiles
  (`realistic_ssp`) are the exception: they model concrete prod wallets
  including the regtest SSP, so REGTEST is intentional and per-group via
  `WalletGroup.Network`. Don't propagate that to tiers without thinking
  through whether the partial-index plans on REGTEST are actually what you
  want to study.
- **Making it an Ent-based seed** — would take 50× longer and solve nothing.
  See "Bypasses Ent entirely" above.
