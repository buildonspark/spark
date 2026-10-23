# Ptah configuration. Ent owns ordinary tables; external SQL owns
# the partitioned signing_nonces parent. The application owns dated children.
data "external_schema" "ent" {
  program     = ["go", "run", "./tools/entschema", "-database", "operator"]
  working_dir = "../.."
  format      = "hcl"
}

data "composite_schema" "operator" {
  schema "public" {
    url = data.external_schema.ent.url
  }
  schema "public" {
    url = "file://external/schema.sql"
  }
}

env "local" {
  src = data.composite_schema.operator.url
  dev = getenv("SPARK_SCHEMA_DEV_URL")
  exclude = ["signing_nonces_[0-9]*"]
  migration {
    dir = "file://migrate/migrations"
  }
  diff {
    skip {
      drop_table = true
    }
  }
}

# The original history predates the separate manual partition cutover.
# Replay every migration, but keep that parent out of automatic authoring.
env "authoring" {
  src = data.composite_schema.operator.url
  dev = getenv("SPARK_SCHEMA_DEV_URL")
  exclude = ["signing_nonces", "signing_nonces_[0-9]*"]
  migration {
    dir = "file://migrate/migrations"
  }
  diff {
    skip {
      drop_table = true
    }
  }
}
