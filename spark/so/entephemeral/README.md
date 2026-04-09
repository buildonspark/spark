# Ephemeral Database (`so/entephemeral`)

`so/entephemeral` is a separate Ent schema/client for data that must not be retained in backup-capable storage.

## Why This Exists

The Signing Operator has obligations to forget certain sensitive material. The main database may be configured for backups (for example to support blue/green deployments or Aurora requirements), so secrets that must be forgettable cannot live there.

The ephemeral database provides a separate storage boundary for this data.

## Scope and Non-Goals

This database is intentionally minimal:

- Keep only data with a strict "must never be backed up" requirement.
- Do not duplicate ordinary business state from the main database.
- Do not add convenience tables that can safely remain in the primary DB.

Today the only table is:

- `signing_keyshare_secrets` (`signing_keyshare_id`, `version`, `secret_share`)

## Main DB vs Ephemeral DB

The main `signing_keyshares` row keeps durable metadata and keyshare state (status, public material, etc).
The secret share bytes are stored in `signing_keyshare_secrets` in the ephemeral DB and are linked by:

- `signing_keyshare_id`
- `version`

There are no cross-database foreign keys; integrity is maintained by application logic.

Current main-DB schema also has:

- nullable `signing_keyshares.secret_share` (legacy/back-compat path)
- nullable `signing_keyshares.secret_version` (active pointer into ephemeral versions)

`secret_version` is the durable link to the active ephemeral secret row.

## Secret Resolution (`GetSecretShare`)

`SigningKeyshare.GetSecretShare(ctx)` is the canonical read path for secret material. Resolution order is:

- `signing_keyshares.secret_share` (main DB, if present)
- cached in-memory `ExternalSecret` on the entity
- ephemeral lookup by `(signing_keyshare_id, secret_version)`

Concurrency/caching behavior:

- `SecretShare` from DB scan is treated as immutable and can be read without locking.
- `ExternalSecret` is mutable cache state and is guarded by `secretMu` to avoid data races and duplicate fetches.
- This gives a single synchronized ephemeral fetch per entity pointer in the common case.

Hydration behavior:

- `HydrateSigningKeyshareSecrets(ctx, keyshares)` batch-loads secrets for keyshares missing main-db `secret_share`.
- It deduplicates lookups by `(signing_keyshare_id, secret_version)` and populates `ExternalSecret` for all matching in-memory pointers (including duplicate pointers to the same keyshare).
- If ephemeral DB context is unavailable when hydration is required, it returns `ErrSigningKeyshareSecretUnavailable`.
- If any requested `(id,version)` is missing in ephemeral storage, hydration fails fast with `ErrSigningKeyshareSecretMissing` and lists missing pairs.

Several key paths call hydration up-front to avoid N+1 secret fetches and to fail deterministically before cryptographic work (`GetKeyPackage(s)`, keyshare aggregation/summing, tweak/fix/recovery paths).

Error behavior:

- Null main secret + nil `secret_version` => missing-secret error.
- Null main secret + no injected ephemeral session/tx provider => unavailable error.
- Nonexistent `(id, version)` in ephemeral store => missing-secret error.

## Versioning Model

Versioning is used to coordinate updates across two independent databases:

- A keyshare tweak increments the signing keyshare version.
- The corresponding secret is written as a new `(signing_keyshare_id, version)` row in the ephemeral DB.
- During update/commit windows, old and new versions may coexist briefly in the ephemeral DB.
- Old versions are cleaned up with best-effort deletion once the new version is safely persisted.

This avoids in-place mutation races and provides deterministic lookup of the secret for a specific main-db version.

When combining keyshares, version information is intentionally discarded:

- aggregate/sum logic sets `SecretVersion = nil` on the result regardless of input versions
- the combined secret is stored directly in `SecretShare`, not as a versioned row

## Secret Rotation and Dual-Write Rollout

This branch introduces helpers that rotate secrets in ephemeral storage and then update main DB pointers:

- `PrepareSigningKeyshareCreateWithSecret(...)` for keyshare creation flows.
- `UpdateSigningKeyshareWithRotatedSecret(...)` for update/rotation flows.

Behavior:

- If ephemeral DB is available, a new version is created in ephemeral DB first, then main DB is updated to point to that version.
- If ephemeral DB is unavailable, logic falls back to main-db `secret_share` only (legacy mode).
- Dual-write to main `secret_share` during ephemeral mode is controlled by knob `spark.so.signing_keyshare.dual_write_secret_share`.
- Batch/loop flows should freeze the rollout decision once per request via `FreezeSigningKeyshareSecretDualWriteDecision(ctx)` so behavior is consistent within that flow.

Cleanup semantics for rotations are best-effort and instrumented:

- On main transaction rollback: newly-created ephemeral version is best-effort deleted.
- On successful main commit: previous ephemeral version is best-effort deleted.
- Cleanup failures are counted in metric `spark_db_ent_signing_keyshare_secret_cleanup_failures_total` with stage/reason attributes.

## Secret Version APIs

`signingkeysharesecret_extension.go` provides explicit helpers for versioned secret lifecycle:

- `GetSigningKeyshareSecretVersion(id, version)` fetches an exact version.
- `GetLatestSigningKeyshareSecretVersionForUpdate(id)` reads the latest version with row locking.
- `AddSigningKeyshareSecretVersion(id, secret)` allocates the next version (`latest + 1`, or `0` if missing).
- `CreateSigningKeyshareSecretVersion(id, version, secret)` inserts an explicit version.
- `DeleteSigningKeyshareSecretVersion(id, version)` removes a specific version.

Behavioral notes:

- `GetSigningKeyshareSecretVersion` and `DeleteSigningKeyshareSecretVersion` return `ErrNoSecretVersion` when the requested version is absent.
- `GetLatestSigningKeyshareSecretVersionForUpdate` returns `(nil, nil)` when no version exists yet (signaling "start from version 0").
- Duplicate `(signing_keyshare_id, version)` inserts fail via the unique index.
- Version overflow (`int32` max) is rejected explicitly.

## Advisory Locking for Version Writes

To serialize concurrent version allocation per `signing_keyshare_id`, writes take a transaction-scoped Postgres advisory lock:

- Lock primitive: `pg_advisory_xact_lock(classid, objid)`
- Key derivation: UUID -> FNV-64a hash -> split into stable `(classid, objid)` int32 pair

The UUID hashing step is deliberate: using FNV-64a over the full UUID avoids collision/pathological contention patterns from simpler folding strategies.

This lock is applied by mutation flows (`Add*`, `Create*`, and latest-for-update path) when running on Postgres. On sqlite (used in unit tests), advisory locking is skipped because `pg_advisory_xact_lock` is unavailable.

## Transaction and Commit Semantics

Cross-database transactions are not atomic. This follows a Saga-style pattern (with compensating actions and explicit divergence handling), not a distributed 2PC transaction:

- Reference: https://microservices.io/patterns/data/saga.html

Current middleware behavior is explicit:

- Start/track main and ephemeral transactions independently.
- Commit ephemeral first.
- If ephemeral commit fails, do not attempt main commit. Even if handler/task logic completed
  successfully, middleware returns an error and discards the success response/result.
- If main commit fails after ephemeral commit, log a divergence error and return an error.

Ephemeral transaction creation is intentionally lazy in the normal request/task path:

- When ephemeral DB is configured, middleware injects an ephemeral session broadly, but only a
  narrow subset of flows actually mutate ephemeral state.
- Some production ephemeral access is pure read-only secret lookup / hydration and should not pay
  the cost of opening a transaction just to query `signing_keyshare_secrets`.
- `GetDbFromContext(...)` is therefore allowed to return a raw ephemeral client for non-locking
  reads, while `GetTxFromContext(...)` is the explicit opt-in for write and locking flows.

Examples:

- Read-only path:
  - `SigningKeyshare.GetSecretShare(...)` and `HydrateSigningKeyshareSecrets(...)` resolve
    secrets through `GetDbFromContext(...)` and query `signing_keyshare_secrets` without forcing
    a transaction.
  - `GetSigningKeyshareSecretVersion(...)` is intentionally implemented as a pure read against the
    context client rather than requiring a transaction.
- Transactional path:
  - `prepareSigningKeyshareSecretRotation(...)` explicitly calls `entephemeral.GetTxFromContext(...)`
    before version allocation / insertion, and **commits the ephemeral transaction itself** before
    registering compensating hooks on the main transaction. This is safe because secret rotation
    only runs in the gRPC/task middleware path (where `EphemeralSession` manages the tx lifecycle),
    never inside the chain watcher's block processing.
  - `GetLatestSigningKeyshareSecretVersionForUpdate(...)`,
    `CreateSigningKeyshareSecretVersion(...)`, and
    `DeleteSigningKeyshareSecretVersion(...)` all require an ephemeral transaction because they
    either lock, write, or both.

This behavior is implemented consistently in:

- gRPC request middleware (`spark/so/grpc/database_middleware.go`)
- task middleware (`spark/so/task/middleware.go`)
- chain watcher block processing (`spark/so/chain/watch_chain.go`)

## Runtime Integration

- Ephemeral DB is configured via `Config.EphemeralDatabasePath`.
- If not configured, operator startup logs that ephemeral DB is disabled and runs without it.
- Health readiness checks include both databases when ephemeral is enabled.
- Separate session/factory types exist for ephemeral context injection and transaction lifecycle (`spark/so/db/session_ephemeral.go`).
- `GetClient` on tx providers returns the underlying client and does not implicitly begin a transaction.
  Explicit transaction creation happens only through `GetOrBeginTx` via session-managed flows.
- Chain watcher is the main exception to lazy transaction creation:
  - `so/chain/watch_chain.go` opens main and ephemeral transactions up front for block processing.
  - It injects a tx-backed ephemeral session into context instead of relying on session-managed lazy
    creation.
  - This is intentional because block handling wants explicit ownership of both transactions across
    the entire block-processing unit of work.
  - Block processing does not invoke secret rotation flows (`prepareSigningKeyshareSecretRotation`,
    `UpdateSigningKeyshareWithRotatedSecret`, `TweakKeyShare`), so there is no conflict with
    those functions committing the ephemeral transaction mid-flow.

## Operational Notes

- For local cluster tooling, ephemeral DB URIs are wired in deployment scripts (`tilt` and minikube deploy script).
- The schema/migrations for this DB are managed under `so/entephemeral/{schema,migrate}` similarly to main Ent schema flow.
