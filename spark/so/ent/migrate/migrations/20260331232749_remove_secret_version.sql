-- atlas:nolint DS103
-- Modify "signing_keyshares" table
ALTER TABLE "signing_keyshares" DROP COLUMN IF EXISTS "secret_version";
