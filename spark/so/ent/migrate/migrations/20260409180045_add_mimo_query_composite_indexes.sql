-- atlas:txmode none

-- Create index "transferreceiver_identity_pubkey_create_time" to table: "transfer_receivers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "transferreceiver_identity_pubkey_create_time" ON "transfer_receivers" ("identity_pubkey", "create_time" DESC);
-- Create index "transfersender_identity_pubkey_create_time" to table: "transfer_senders"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "transfersender_identity_pubkey_create_time" ON "transfer_senders" ("identity_pubkey", "create_time" DESC);
