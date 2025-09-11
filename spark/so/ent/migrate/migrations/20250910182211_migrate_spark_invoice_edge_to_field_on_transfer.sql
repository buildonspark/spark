-- Rename a column from "transfer_spark_invoice" to "spark_invoice_id"
ALTER TABLE "transfers" RENAME COLUMN "transfer_spark_invoice" TO "spark_invoice_id";
