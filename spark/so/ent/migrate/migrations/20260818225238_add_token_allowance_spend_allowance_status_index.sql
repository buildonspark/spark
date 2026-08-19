-- atlas:txmode none

-- Create index "tokenallowancespend_token_allowance_id_status" to table: "token_allowance_spends"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "tokenallowancespend_token_allowance_id_status" ON "token_allowance_spends" ("token_allowance_id", "status");
