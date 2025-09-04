-- Modify "transfers" table
ALTER TABLE "transfers" ADD COLUMN "transfer_spark_invoice" uuid NULL, ADD CONSTRAINT "transfers_spark_invoices_spark_invoice" FOREIGN KEY ("transfer_spark_invoice") REFERENCES "spark_invoices" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
