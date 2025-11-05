-- atlas:txmode none

-- Create index "idx_transfers_recv_status_update" to table: "transfers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS
    "idx_transfers_recv_status_update"
ON "transfers" ("receiver_identity_pubkey", "status", "update_time" DESC);
