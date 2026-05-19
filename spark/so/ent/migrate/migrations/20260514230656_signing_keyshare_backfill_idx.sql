-- atlas:txmode none

-- Create index "idx_signing_keyshares_missing_secret_version_id" to table: "signing_keyshares"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_signing_keyshares_missing_secret_version_id" ON "signing_keyshares" ("id") WHERE (secret_version IS NULL);
