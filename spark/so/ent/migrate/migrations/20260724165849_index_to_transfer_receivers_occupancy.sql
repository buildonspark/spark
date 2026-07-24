-- atlas:txmode none

-- Create index "idx_transferreceiver_status_type_time" to table: "transfer_receivers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transferreceiver_status_type_time" ON "transfer_receivers" ("status", "transfer_type", "update_time", "transfer_id");
