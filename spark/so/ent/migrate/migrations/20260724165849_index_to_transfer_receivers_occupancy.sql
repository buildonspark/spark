-- atlas:txmode none

-- A failed CREATE INDEX CONCURRENTLY leaves an INVALID index behind, and a
-- bare IF NOT EXISTS retry would keep it; dropping first makes the retry
-- rebuild instead. No-op on a clean first run.
DROP INDEX CONCURRENTLY IF EXISTS "idx_transferreceiver_status_type_time";
-- Create index "idx_transferreceiver_status_type_time" to table: "transfer_receivers"
CREATE INDEX CONCURRENTLY "idx_transferreceiver_status_type_time" ON "transfer_receivers" ("status", "transfer_type", "update_time", "transfer_id");
