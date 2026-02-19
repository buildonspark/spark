# Local Atlas configuration for migration generation
# Run atlas commands from this directory (spark/so/ent/)
#
# NOTE: The signing_nonce table is excluded from Atlas management
# and migrated manually. See external/schema.sql and README.md.

env "local" {
  # Source schema (desired state) - Ent-generated schemas only
  # Excludes manually-managed tables (signing_nonce)
  src = "ent://schema"

  # Dev database for Atlas to use for normalization and diffing
  dev = "docker://postgres/15/test?search_path=public"

  # Migration directory
  migration {
    dir = "file://migrate/migrations"
  }

  # Exclude manually-managed tables from Atlas
  diff {
    skip {
      drop_table = true  # Don't generate DROP TABLE for removed schemas
    }
  }
}
