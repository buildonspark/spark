-- atlas:txmode none

-- Create index "idx_transfers_outgoing_in_flight_sender_pubkey_time" to table: "transfers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transfers_outgoing_in_flight_sender_pubkey_time"
ON "transfers" ("sender_identity_pubkey", "create_time" DESC, "id" DESC)
WHERE status IN ('SENDER_INITIATED', 'SENDER_INITIATED_COORDINATOR', 'APPLYING_SENDER_KEY_TWEAK', 'SENDER_KEY_TWEAK_PENDING');
