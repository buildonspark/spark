-- Create index "idx_transfers_spark_invoice_completed" to table: "transfers"
CREATE UNIQUE INDEX "idx_transfers_spark_invoice_completed" ON "transfers" ("spark_invoice_id") WHERE ((status)::text = ANY (ARRAY['SENDER_KEY_TWEAKED'::text, 'RECEIVER_KEY_TWEAKED'::text, 'RECEIVER_KEY_TWEAK_LOCKED'::text, 'RECEIVER_KEY_TWEAK_APPLIED'::text, 'RECEIVER_REFUND_SIGNED'::text, 'COMPLETED'::text]));
-- Create index "idx_transfers_spark_invoice_pending" to table: "transfers"
CREATE UNIQUE INDEX "idx_transfers_spark_invoice_pending" ON "transfers" ("spark_invoice_id") WHERE ((status)::text = ANY (ARRAY['SENDER_KEY_TWEAK_PENDING'::text, 'SENDER_INITIATED_COORDINATOR'::text]));
