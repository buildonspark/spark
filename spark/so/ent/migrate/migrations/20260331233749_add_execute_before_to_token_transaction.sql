-- Modify "token_transactions" table
ALTER TABLE "token_transactions" ADD COLUMN "execute_before" timestamptz NULL;
