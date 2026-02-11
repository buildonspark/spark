-- atlas:txmode none

-- Drop index "transfer_status_expiry_time_type" from table: "transfers"
DROP INDEX CONCURRENTLY "transfer_status_expiry_time_type";
-- Create index "idx_transfers_cancel_preimage_swap" to table: "transfers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transfers_cancel_preimage_swap" ON "transfers" ("status", "expiry_time", "type") WHERE (((status)::text = 'SENDER_KEY_TWEAK_PENDING'::text) AND ((type)::text = 'PREIMAGE_SWAP'::text) AND (expiry_time <> '1970-01-01 00:00:00+00'::timestamp with time zone));
-- Create index "idx_transfers_cancel_sender_initiated" to table: "transfers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transfers_cancel_sender_initiated" ON "transfers" ("status", "expiry_time", "type") WHERE (((status)::text = 'SENDER_INITIATED'::text) AND ((type)::text <> 'COUNTER_SWAP'::text) AND (expiry_time <> '1970-01-01 00:00:00+00'::timestamp with time zone));
