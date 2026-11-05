# The ephemeral database keeps its own desired schema and migration history.
data "external_schema" "ent" {
  program     = ["go", "run", "./tools/entschema", "-database", "ephemeral"]
  working_dir = "../.."
  format      = "hcl"
}

env "local" {
  src = data.external_schema.ent.url
  dev = getenv("SPARK_SCHEMA_DEV_URL")
  migration {
    dir = "file://migrate/migrations"
  }
  diff {
    skip {
      drop_table = true
    }
  }
}
