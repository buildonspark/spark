-- Modify "token_transactions" table
ALTER TABLE "token_transactions" ADD COLUMN "expiry_time" timestamptz NULL;
