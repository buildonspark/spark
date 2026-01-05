-- Modify "signing_nonces" table
-- atlas:nolint destructive
ALTER TABLE "signing_nonces" DROP COLUMN "message";
