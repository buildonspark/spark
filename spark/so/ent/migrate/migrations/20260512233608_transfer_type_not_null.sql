-- atlas:txmode none

-- Promote transfer_type to NOT NULL without an AccessExclusive table scan.
-- Pattern: CHECK ... NOT VALID -> VALIDATE CONSTRAINT (ShareUpdateExclusive,
-- reads + writes proceed) -> SET NOT NULL (Postgres >=12 uses the validated
-- CHECK to skip the rewrite/scan) -> DROP redundant CHECK so the runtime
-- catalog matches the Ent schema.
--
-- Each statement auto-commits (txmode none). If VALIDATE CONSTRAINT fails
-- because a tail row is still NULL, the migration aborts mid-file and
-- subsequent statements never run. The leading DROP CONSTRAINT IF EXISTS
-- on each table makes the migration re-runnable after the offending data
-- is fixed; without it, the retry would error on ADD CONSTRAINT.

-- transfer_senders
ALTER TABLE "transfer_senders" DROP CONSTRAINT IF EXISTS "transfer_senders_transfer_type_not_null";
ALTER TABLE "transfer_senders" ADD CONSTRAINT "transfer_senders_transfer_type_not_null" CHECK ("transfer_type" IS NOT NULL) NOT VALID;
ALTER TABLE "transfer_senders" VALIDATE CONSTRAINT "transfer_senders_transfer_type_not_null";
ALTER TABLE "transfer_senders" ALTER COLUMN "transfer_type" SET NOT NULL;
ALTER TABLE "transfer_senders" DROP CONSTRAINT "transfer_senders_transfer_type_not_null";

-- transfer_receivers
ALTER TABLE "transfer_receivers" DROP CONSTRAINT IF EXISTS "transfer_receivers_transfer_type_not_null";
ALTER TABLE "transfer_receivers" ADD CONSTRAINT "transfer_receivers_transfer_type_not_null" CHECK ("transfer_type" IS NOT NULL) NOT VALID;
ALTER TABLE "transfer_receivers" VALIDATE CONSTRAINT "transfer_receivers_transfer_type_not_null";
ALTER TABLE "transfer_receivers" ALTER COLUMN "transfer_type" SET NOT NULL;
ALTER TABLE "transfer_receivers" DROP CONSTRAINT "transfer_receivers_transfer_type_not_null";
