-- atlas:txmode none

-- Drop legacy partial index "idx_transferreceiver_pending_pubkey_time" from
-- table: "transfer_receivers". Superseded by
-- idx_transferreceiver_claim_pending_pubkey_time after the
-- INITIATED → RECEIVER_CLAIM_PENDING rollout (SP-2923).
DROP INDEX CONCURRENTLY IF EXISTS "idx_transferreceiver_pending_pubkey_time";
