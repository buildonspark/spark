-- atlas:txmode none

-- Create index "idx_transferreceiver_claim_pending_pubkey_time" to table: "transfer_receivers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transferreceiver_claim_pending_pubkey_time"
ON "transfer_receivers" ("identity_pubkey", "create_time" DESC, "transfer_id" DESC)
WHERE status IN ('RECEIVER_CLAIM_PENDING', 'RECEIVER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAK_LOCKED', 'RECEIVER_KEY_TWEAK_APPLIED', 'RECEIVER_REFUND_SIGNED');
