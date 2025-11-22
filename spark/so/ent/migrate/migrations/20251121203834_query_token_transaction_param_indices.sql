-- Drop index "tokenoutput_token_identifier_status" from table: "token_outputs"
DROP INDEX "tokenoutput_token_identifier_status";
-- Create index "tokenoutput_token_identifier_status" to table: "token_outputs"
CREATE INDEX "tokenoutput_token_identifier_status" ON "token_outputs" ("token_identifier", "status") INCLUDE ("token_output_output_created_token_transaction", "token_output_output_spent_token_transaction");
-- Create index "tokenoutput_token_public_key_status" to table: "token_outputs"
CREATE INDEX "tokenoutput_token_public_key_status" ON "token_outputs" ("token_public_key", "status") INCLUDE ("token_output_output_created_token_transaction", "token_output_output_spent_token_transaction");
