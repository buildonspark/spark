-- atlas:nolint DS103
-- Columns are nullable and no longer written to (deprecated in prior PRs).
ALTER TABLE "partners" DROP COLUMN "partner_id", DROP COLUMN "partner_name", DROP COLUMN "jwt_public_key";
