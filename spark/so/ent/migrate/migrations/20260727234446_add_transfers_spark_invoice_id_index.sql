-- atlas:txmode none

-- Create index "idx_transfers_spark_invoice_id" to table: "transfers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_transfers_spark_invoice_id" ON "transfers" ("spark_invoice_id") WHERE (spark_invoice_id IS NOT NULL);
