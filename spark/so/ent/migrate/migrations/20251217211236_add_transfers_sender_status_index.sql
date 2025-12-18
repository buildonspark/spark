-- Create index "idx_transfers_sender_status_create" to table: "transfers"
CREATE INDEX "idx_transfers_sender_status_create" ON "transfers" ("sender_identity_pubkey", "status", "create_time" DESC);
