-- Modify "transfer_leafs" table
ALTER TABLE "transfer_leafs" ADD COLUMN "intermediate_refund_txid" bytea NULL, ADD COLUMN "intermediate_direct_refund_txid" bytea NULL, ADD COLUMN "intermediate_direct_from_cpfp_refund_txid" bytea NULL;
-- Create index "transferleaf_intermediate_direct_from_cpfp_refund_txid" to table: "transfer_leafs"
CREATE INDEX "transferleaf_intermediate_direct_from_cpfp_refund_txid" ON "transfer_leafs" ("intermediate_direct_from_cpfp_refund_txid") WHERE (intermediate_direct_from_cpfp_refund_txid IS NOT NULL);
-- Create index "transferleaf_intermediate_direct_refund_txid" to table: "transfer_leafs"
CREATE INDEX "transferleaf_intermediate_direct_refund_txid" ON "transfer_leafs" ("intermediate_direct_refund_txid") WHERE (intermediate_direct_refund_txid IS NOT NULL);
-- Create index "transferleaf_intermediate_refund_txid" to table: "transfer_leafs"
CREATE INDEX "transferleaf_intermediate_refund_txid" ON "transfer_leafs" ("intermediate_refund_txid") WHERE (intermediate_refund_txid IS NOT NULL);
