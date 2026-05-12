-- atlas:txmode none

-- Composite drives queryAll receiver-arm with type filter via leading-equality + ordered top-N.
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transferreceiver_pubkey_type_time" ON "transfer_receivers" ("identity_pubkey", "transfer_type", "create_time" DESC, "transfer_id" DESC);

-- Composite drives queryAll sender-arm with type filter via leading-equality + ordered top-N.
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transfersender_pubkey_type_time" ON "transfer_senders" ("identity_pubkey", "transfer_type", "create_time" DESC, "transfer_id" DESC);
