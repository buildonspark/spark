-- atlas:txmode none

-- Drop index "idx_transfers_recv_status_update" from table: "transfers"
DROP INDEX CONCURRENTLY "idx_transfers_recv_status_update";
-- Create index "idx_transfers_recv_status_create" to table: "transfers"
CREATE INDEX CONCURRENTLY
    "idx_transfers_recv_status_create"
ON "transfers" ("receiver_identity_pubkey", "status", "create_time" DESC);
