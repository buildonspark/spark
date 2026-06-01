-- atlas:txmode none

-- Create index "idx_signing_keyshares_clearable_secret_share_id" to table: "signing_keyshares"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_signing_keyshares_clearable_secret_share_id" ON "signing_keyshares" ("id") WHERE ((secret_share IS NOT NULL) AND (secret_version IS NOT NULL));
