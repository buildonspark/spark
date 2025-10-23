-- Modify "transfer_leafs" table
ALTER TABLE "transfer_leafs" ADD COLUMN "intermediate_refund_timelock" bigint NULL, ADD COLUMN "intermediate_direct_refund_timelock" bigint NULL, ADD COLUMN "intermediate_direct_from_cpfp_refund_timelock" bigint NULL;
-- Create index "transferleaf_intermediate_direct_from_cpfp_refund_timelock" to table: "transfer_leafs"
CREATE INDEX "transferleaf_intermediate_direct_from_cpfp_refund_timelock" ON "transfer_leafs" ("intermediate_direct_from_cpfp_refund_timelock") WHERE (intermediate_direct_from_cpfp_refund_timelock IS NOT NULL);
-- Create index "transferleaf_intermediate_direct_refund_timelock" to table: "transfer_leafs"
CREATE INDEX "transferleaf_intermediate_direct_refund_timelock" ON "transfer_leafs" ("intermediate_direct_refund_timelock") WHERE (intermediate_direct_refund_timelock IS NOT NULL);
-- Create index "transferleaf_intermediate_refund_timelock" to table: "transfer_leafs"
CREATE INDEX "transferleaf_intermediate_refund_timelock" ON "transfer_leafs" ("intermediate_refund_timelock") WHERE (intermediate_refund_timelock IS NOT NULL);
