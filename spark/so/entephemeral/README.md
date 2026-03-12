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

## Versioning Model

Versioning is used to coordinate updates across two independent databases:

- A keyshare tweak increments the signing keyshare version.
- The corresponding secret is written as a new `(signing_keyshare_id, version)` row in the ephemeral DB.
- During update/commit windows, old and new versions may coexist briefly in the ephemeral DB.
- Old versions are cleaned up with best-effort deletion once the new version is safely persisted.

This avoids in-place mutation races and provides deterministic lookup of the secret for a specific main-db version.

## Transaction and Commit Semantics

Cross-database transactions are not atomic. This follows a Saga-style pattern (with compensating actions and explicit divergence handling), not a distributed 2PC transaction:

- Reference: https://microservices.io/patterns/data/saga.html

Current middleware behavior is explicit:

- Start/track main and ephemeral transactions independently.
- Commit ephemeral first.
- If ephemeral commit fails, do not attempt main commit. Even if handler/task logic completed
  successfully, middleware returns an error and discards the success response/result.
- If main commit fails after ephemeral commit, log a divergence error and return an error.

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

## Operational Notes

- For local cluster tooling, ephemeral DB URIs are wired in deployment scripts (`tilt` and minikube deploy script).
- The schema/migrations for this DB are managed under `so/entephemeral/{schema,migrate}` similarly to main Ent schema flow.
