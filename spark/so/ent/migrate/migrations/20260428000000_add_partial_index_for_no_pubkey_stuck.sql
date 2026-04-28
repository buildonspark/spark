-- atlas:txmode none

-- Create index "idx_transferreceiver_stuck_create_time" to table: "transfer_receivers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transferreceiver_stuck_create_time" ON "transfer_receivers" ("create_time" DESC, "transfer_id" DESC) WHERE status IN ('RECEIVER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAK_LOCKED', 'RECEIVER_KEY_TWEAK_APPLIED', 'RECEIVER_REFUND_SIGNED');
