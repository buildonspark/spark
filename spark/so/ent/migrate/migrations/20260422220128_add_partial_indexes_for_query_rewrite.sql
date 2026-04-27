-- atlas:txmode none

-- Create index "idx_transferreceiver_pending_pubkey_time" to table: "transfer_receivers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transferreceiver_pending_pubkey_time" ON "transfer_receivers" ("identity_pubkey", "create_time" DESC, "transfer_id" DESC) WHERE ((status)::text = ANY (ARRAY['INITIATED'::text, 'RECEIVER_KEY_TWEAKED'::text, 'RECEIVER_KEY_TWEAK_LOCKED'::text, 'RECEIVER_KEY_TWEAK_APPLIED'::text, 'RECEIVER_REFUND_SIGNED'::text]));
-- Create index "idx_transfers_active_network_time" to table: "transfers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transfers_active_network_time" ON "transfers" ("network", "create_time" DESC, "id" DESC) WHERE ((status)::text = ANY (ARRAY['SENDER_INITIATED'::text, 'SENDER_INITIATED_COORDINATOR'::text, 'SENDER_KEY_TWEAK_PENDING'::text, 'SENDER_KEY_TWEAKED'::text, 'RECEIVER_KEY_TWEAKED'::text, 'RECEIVER_KEY_TWEAK_LOCKED'::text, 'RECEIVER_KEY_TWEAK_APPLIED'::text, 'RECEIVER_REFUND_SIGNED'::text]));
