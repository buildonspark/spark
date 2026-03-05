# Local Atlas configuration for migration generation
# Run atlas commands from this directory (spark/so/entephemeral/)
#
# NOTE: drop_table is skipped as a safety guard to avoid accidentally
# generating DROP TABLE statements for tables removed from the schema.

env "local" {
  # Source schema (desired state) - Ent-generated schemas only
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
