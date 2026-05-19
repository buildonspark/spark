-- atlas:txmode none

-- Create index "idx_transferreceiver_initiated_pubkey_type_time" to table: "transfer_receivers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transferreceiver_initiated_pubkey_type_time"
ON "transfer_receivers" ("identity_pubkey", "transfer_type", "create_time" DESC, "transfer_id" DESC)
WHERE status = 'INITIATED';
