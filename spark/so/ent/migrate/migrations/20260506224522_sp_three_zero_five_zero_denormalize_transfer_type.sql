-- Modify "transfer_receivers" table — additive nullable column (no rewrite on PG 11+).
ALTER TABLE "transfer_receivers" ADD COLUMN IF NOT EXISTS "transfer_type" character varying NULL;

-- Modify "transfer_senders" table — additive nullable column.
ALTER TABLE "transfer_senders" ADD COLUMN IF NOT EXISTS "transfer_type" character varying NULL;
