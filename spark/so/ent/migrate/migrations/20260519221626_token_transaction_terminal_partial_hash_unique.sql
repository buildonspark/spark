-- atlas:txmode none

DROP INDEX CONCURRENTLY IF EXISTS "tokentransaction_terminal_partial_hash_unique";

-- Create index "tokentransaction_terminal_partial_hash_unique" to table: "token_transactions"
-- Exclude known pre-existing prod duplicates. The retained/newer duplicate on each SO remains indexed,
-- so future terminal rows for these partial hashes still conflict.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "tokentransaction_terminal_partial_hash_unique" ON "token_transactions" ("partial_token_transaction_hash") WHERE status IN ('REVEALED', 'FINALIZED') AND id NOT IN ('019c9bc6-21ef-70a0-ac25-a663d3bff645', '019c9bc6-2205-7198-9128-daa9c7633622', '019c9bc6-220c-790f-a798-db2f8365338a');
