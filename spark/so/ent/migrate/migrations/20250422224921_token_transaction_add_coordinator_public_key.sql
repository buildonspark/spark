-- Modify "token_transactions" table
ALTER TABLE "token_transactions" ADD COLUMN "coordinator_public_key" bytea NULL;
-- Create index "tokentransaction_partial_token_transaction_hash" to table: "token_transactions"
CREATE INDEX "tokentransaction_partial_token_transaction_hash" ON "token_transactions" ("partial_token_transaction_hash");
