-- Drop index "tokentransaction_update_time" from table: "token_transactions"
DROP INDEX "tokentransaction_update_time";
-- Create index "tokentransaction_create_time" to table: "token_transactions"
CREATE INDEX "tokentransaction_create_time" ON "token_transactions" ("create_time");
