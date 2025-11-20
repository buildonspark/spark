-- atlas:txmode none

-- Create index "tokenoutput_owner_public_key_token_identifier_status" to table: "token_outputs"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "tokenoutput_owner_public_key_token_identifier_status" ON "token_outputs" ("owner_public_key", "token_identifier", "status") INCLUDE ("token_output_output_created_token_transaction", "token_output_output_spent_token_transaction");
