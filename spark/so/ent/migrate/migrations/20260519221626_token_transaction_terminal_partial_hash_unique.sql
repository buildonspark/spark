-- atlas:txmode none

-- Create index "tokentransaction_terminal_partial_hash_unique" to table: "token_transactions"
CREATE UNIQUE INDEX CONCURRENTLY "tokentransaction_terminal_partial_hash_unique" ON "token_transactions" ("partial_token_transaction_hash") WHERE status IN ('REVEALED', 'FINALIZED');
