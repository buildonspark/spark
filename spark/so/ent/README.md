# Ent Schema and Migration Management

This directory contains the Ent schema definitions and migration tooling for the Spark Operator database.

## Overview

We use [Ent](https://entgo.io/) for schema definitions and code generation, combined with [Atlas](https://atlasgo.io/) for database migrations. Most tables are managed automatically by Ent/Atlas, but some tables require manual management due to advanced database features.

## Directory Structure

```
ent/
├── schema/              # Ent schema definitions (Go code)
├── external/            # Manual SQL schemas for special tables
│   └── schema.sql      # Externally-managed table definitions
├── migrate/
│   └── migrations/     # Atlas migration files
└── README.md           # This file
```

## Migration Workflows

### Standard Workflow (Most Tables)

For tables managed by Ent/Atlas:

1. **Modify Ent schema** in `schema/*.go`
2. **Regenerate Ent code**: `make ent`
3. **Generate migration**:
   ```bash
   mise atlas-diff my_migration_name.sql
   ```
4. **Review generated migration** in `migrate/migrations/`
5. **Test on staging** database
6. **Deploy** - Atlas automatically applies on startup

### External Table Workflow (Manual Management)

Some tables require manual management due to advanced features that Ent/Atlas cannot handle:
- Partitioning
- Advanced indexes
- Custom constraints

**Example: `signing_nonces` (partitioned table)**

#### Why External?

The `signing_nonces` table uses:
- **Range partitioning** by `id` (UUIDv7, 24-hour intervals)
- **Simple primary key** `(id)` - cleaner than composite key
- **Dynamic partition management** (created/dropped by application code)
- **UUID-based boundaries** - leverages UUIDv7's time-ordering property

These features can't be expressed in Ent's schema language and require manual SQL management.

#### Setup

**1. Ent Schema (generates Go types)**

Keep the normal Ent schema in `schema/signing_nonce.go`:
- Defines fields and types
- Generates Go client code
- Primary key shown as `(id)` for simplicity (composite PK is transparent to application)

**2. External SQL Schema (documentation)**

Document the actual database structure in `external/schema.sql`:
```sql
-- This file is for DOCUMENTATION ONLY
-- Atlas does not use it (composite schemas require Atlas Pro)

CREATE TABLE signing_nonces (
    id                UUID NOT NULL PRIMARY KEY,
    create_time       TIMESTAMP WITH TIME ZONE NOT NULL,
    -- ... other fields ...
) PARTITION BY RANGE (id);  -- Partition by UUIDv7 id
```

**Note**: This file documents the real structure but Atlas ignores it. We accept schema drift between Ent schema and actual database for manually-managed tables.

**Why partition by `id` (UUIDv7)?**
- Simple `PRIMARY KEY (id)` - no composite key needed
- UUIDv7 encodes timestamp in first 48 bits, enabling time-based partitions
- Current cleanup query uses `WHERE id < cutoff_uuid` - benefits from partition pruning
- UNIQUE constraint on `id` works (would fail with `create_time` partitioning)

**3. Atlas Exclusion**

Atlas is configured to skip certain operations on external tables:
```hcl
# In atlas.hcl
diff {
  skip {
    drop_table = true  # Don't generate DROP for removed Ent schemas
  }
}
```

This prevents Atlas from trying to "fix" manually-managed tables.

#### Migration Process

For external tables, migrations are manual:

**1. Create migration script**

Example: `scripts/migrate_signing_nonce_to_partitioned.sql`
- Handles data migration
- Creates partitions
- Atomic cutover

**2. Run manually at chosen time**
```bash
# Test on staging
psql -h staging-db < scripts/migrate_signing_nonce_to_partitioned.sql

# Run on production during maintenance window
psql -h prod-db < scripts/migrate_signing_nonce_to_partitioned.sql
```

**3. Deploy code**

Deploy application code that includes:
- Updated `external/schema.sql` (documents new structure)
- Application code that works with new structure
- Partition management functions

**4. Accept schema drift**

Atlas will see drift between Ent schema and actual database:
- **Ent schema**: Defines `signing_nonces` with PK `(id)` (no partitioning)
- **Actual database**: Has PK `(id)` + RANGE partitioning by `id`
- This drift is **expected and acceptable** for manually-managed tables

Atlas won't try to "fix" it because `drop_table` skip is configured.

**Note**: The primary key is the same in both schemas (`id`), but the database adds partitioning which Ent doesn't know about. This is transparent to application code.

#### Why Keep Both Schemas?

**Ent schema (`schema/signing_nonce.go`)**:
- ✅ Generates Go types and client code
- ✅ Used by application for queries
- ✅ Simple PK definition `(id)` - sufficient for app code
- ❌ Doesn't reflect actual database structure

**External SQL schema (`external/schema.sql`)**:
- ✅ Documents actual database structure
- ✅ Shows PK `(id)` + UUID-based partitioning
- ❌ **NOT used by Atlas** (would require Atlas Pro for composite schemas)
- ❌ For documentation only

The partitioning is **completely transparent to application code** - queries work normally whether by `id`, `nonce_commitment`, or any other column. The Ent schema generates correct Go types even though it doesn't know about partitioning.

## Atlas Configuration

### Local Development

Use the `mise` task for generating migrations:

```bash
# Generate migration from Ent schemas only
mise atlas-diff my_migration_name.sql

# Hash migrations after manual edits
mise atlas-hash
```

This runs:
```bash
cd so/ent && atlas migrate diff "$@" --env local
```

The `atlas.hcl` configuration:
```hcl
env "local" {
  src = "ent://schema"  # Ent schemas only
  dev = "docker://postgres/15/test?search_path=public"
  migration {
    dir = "file://migrate/migrations"
  }
  diff {
    skip {
      drop_table = true  # Don't DROP manually-managed tables
    }
  }
}
```

**Note**: Composite schemas (mixing Ent + SQL) require Atlas Pro. We use Ent-only schema and accept drift on manually-managed tables.

### Production

Atlas configuration is deployed via Helm ConfigMap (in ops repo):
```hcl
env "aws" {
  url = "postgres://..."
  migration {
    dir = "file:///opt/spark/migrations"
  }
  diff {
    skip {
      drop_table = true  # Don't DROP manually-managed tables
    }
  }
}
```

## Adding a New External Table

If you need to manage a new table externally:

**1. Add to `external/schema.sql`**
```sql
-- Add your table definition
CREATE TABLE my_special_table (
    ...
) PARTITION BY ... -- or other special feature
```

**2. Keep or create Ent schema**

You can keep the Ent schema to generate Go types:
- Define fields normally
- Don't worry about special DB features (partitioning, etc.)
- Ent generates client code, SQL schema defines actual structure

**3. Create manual migration script**

For initial creation or conversion:
- Write SQL script in `scripts/`
- Test on staging
- Run manually on production
- Deploy code afterward

**4. Application manages special features**

For dynamic features like partitions:
- Add management code (e.g., `PurgeAndCreateSigningNoncePartitions()`)
- Run periodically via cron/task

## Best Practices

### When to Use External Tables

Use external management when:
- ✅ **Partitioning** (range, list, hash)
- ✅ **Advanced indexes** (partial, expression-based)
- ✅ **Custom constraints** not expressible in Ent
- ✅ **Performance-critical structures** needing manual tuning

Keep using Ent/Atlas when:
- ❌ Standard tables with simple indexes
- ❌ Foreign key relationships
- ❌ Basic constraints (NOT NULL, UNIQUE, CHECK)

### Migration Safety

For risky migrations (large tables, partitioning, etc.):

1. **Test thoroughly** on staging with production-like data
2. **Write manual scripts** with:
   - Bulk copy phase (no locks)
   - Atomic cutover (brief lock)
   - Verification steps
   - Rollback plan
3. **Schedule maintenance window** if needed
4. **Monitor closely** after deployment

### External Schema Documentation

Always document in `external/schema.sql`:
- **Why** the table is external
- **What** special features it uses
- **How** it's managed (which functions, scripts)
- **References** to related code

Example:
```sql
-- External table: signing_nonces
-- Reason: Uses range partitioning by id (UUIDv7)
-- Management: PurgeAndCreateSigningNoncesPartitions() in signingnonce_extension.go
-- Migration: scripts/migrate_signing_nonce_to_partitioned.sql
```

## Troubleshooting

### Atlas Detects Drift

If Atlas shows schema drift for external tables:

**Symptom**: Atlas wants to create a migration for an external table

**Cause**: Database doesn't match `external/schema.sql`

**Solution**:
1. Check if manual migration ran correctly
2. Update `external/schema.sql` to match actual DB if intentional
3. Ensure composite schema is configured correctly

### Ent Code Generation Fails

**Symptom**: `make ent` fails after schema changes

**Cause**: Schema syntax error or missing dependency

**Solution**:
1. Check Ent schema syntax
2. Ensure all imports are correct
3. Run `go mod tidy` if needed

### Migration Conflicts

**Symptom**: Manual migration conflicts with Atlas migration

**Cause**: Timing - both tried to modify same table

**Solution**:
1. External tables should NEVER have Atlas migrations
2. Ensure table is in `external/schema.sql`
3. Remove any Atlas migrations for external tables

## Further Reading

- [Ent Documentation](https://entgo.io/docs/getting-started)
- [Atlas Documentation](https://atlasgo.io/getting-started)
- [Atlas Composite Schemas](https://atlasgo.io/atlas-schema/projects#data-source-composite_schema)
- [Postgres Partitioning](https://www.postgresql.org/docs/current/ddl-partitioning.html)
