# Ent Schema and Migration Management

Ent defines the Go clients and ordinary database tables. Ptah combines those
tables with the partitioned parent in `external/schema.sql`. Spark owns the
dated `signing_nonces_YYYYMMDD` children through
`PurgeAndCreateSigningNoncePartitions`.

## Pinned Ptah version

The root mise configuration and Dockerfile pin the same Ptah release, v0.15.0.
It replays migrations in a session-local temporary schema and applies the
environment's migration diff exclusions. The SQL migration format, original
files, and `atlas.sum` remain unchanged.

The Ent exporter in `tools/entschema` uses Ent's PostgreSQL schema conversion
and the open-source Atlas HCL marshaler. It omits `signing_nonces` from the
operator schema export only. Ent still generates that table's Go client. The
SQL source supplies its partitioned definition to the composite schema.

## Author a migration

From `spark/`, after changing an ordinary Ent schema:

```bash
go generate ./so/ent
mise ptah-diff describe_change
```

For the ephemeral database:

```bash
go generate ./so/entephemeral
mise ptah-ephemeral-diff describe_change
```

Review the new SQL before applying it. The hash tasks are `mise ptah-hash` and
`mise ptah-ephemeral-hash`. Existing migration files must not be rewritten.

Set `SPARK_SCHEMA_DEV_URL` to a disposable PostgreSQL database with
`search_path=public`. Enable external schema execution with
`PTAH_ALLOW_EXTERNAL_SCHEMA=1`. The operator history contains a `DO` block, so
replay also requires `PTAH_DEV_SERVER_DISPOSABLE=1` on a dedicated disposable
server. Never make that declaration for a shared server or an application
database. Ptah replays the entire history; the temporary tables remain local
to the replay connection and disappear when that connection is discarded.

## Compare the live partitioned schema

The operator's `local` environment includes the external parent and excludes
only Spark's dated children:

```bash
cd so/ent
ptah-compat schema diff --env local --from "$OPERATOR_DB_URL" --to env://src --schema public
```

The `authoring` environment also excludes the parent from automatic migration
generation. The original operator history creates an ordinary parent and
predates the separate manual partition cutover. Every historical SQL statement
still runs during replay. The exclusion applies to schema comparison, not to
execution of migration files.

Converting an existing ordinary table to a partitioned table requires a
reviewed manual cutover with a data-copy, validation, and rollback plan. The
full live comparison refuses an ordinary/partitioned mismatch or a changed
partition key. The `drop_table` guard alone is not a partition conversion plan.
Additive changes to a correctly partitioned parent can use a reviewed Ptah
plan; PostgreSQL propagates those changes to the existing children. Spark
continues to create and retire dated children.

## Apply migrations

The entrypoint applies the operator history before the ephemeral history:

```bash
ptah-compat migrate apply --dir file://so/ent/migrate/migrations --url "$OPERATOR_DB_URL"
ptah-compat migrate apply --dir file://so/entephemeral/migrate/migrations --url "$EPHEMERAL_DB_URL"
```

The Docker image builds the pinned `ptah-compat` binary from its Go module.
The pgtestdb adapter in `so/db/ptah_migrator.go` runs the same executable and
includes its version in the template hash. No executable alias is needed.

Use explicit columns in raw SQL instead of `SELECT *` or `RETURNING *`.
Adding columns can otherwise invalidate the result shape of cached prepared
statements on existing connections.
