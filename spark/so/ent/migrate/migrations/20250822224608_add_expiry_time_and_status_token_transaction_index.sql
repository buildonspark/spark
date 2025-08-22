-- Create index "tokentransaction_expiry_time_status" to table: "token_transactions"
CREATE INDEX "tokentransaction_expiry_time_status" ON "token_transactions" ("expiry_time", "status");
