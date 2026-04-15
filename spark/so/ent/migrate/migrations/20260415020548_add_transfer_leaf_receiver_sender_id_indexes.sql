-- atlas:txmode none

-- Create index "transferleaf_transfer_receiver_id" to table: "transfer_leafs"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "transferleaf_transfer_receiver_id" ON "transfer_leafs" ("transfer_receiver_id") WHERE (transfer_receiver_id IS NOT NULL);
-- Create index "transferleaf_transfer_sender_id" to table: "transfer_leafs"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "transferleaf_transfer_sender_id" ON "transfer_leafs" ("transfer_sender_id") WHERE (transfer_sender_id IS NOT NULL);
