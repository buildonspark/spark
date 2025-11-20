-- Modify "token_transactions" table
ALTER TABLE "token_transactions" ADD COLUMN "validity_duration_seconds" bigint NULL;
