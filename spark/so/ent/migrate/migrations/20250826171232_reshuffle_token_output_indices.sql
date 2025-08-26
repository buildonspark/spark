-- Drop index "tokenoutput_owner_public_key_token_identifier_status" from table: "token_outputs"
DROP INDEX "tokenoutput_owner_public_key_token_identifier_status";
-- Drop index "tokenoutput_owner_public_key_token_public_key_status_network" from table: "token_outputs"
DROP INDEX "tokenoutput_owner_public_key_token_public_key_status_network";
-- Create index "tokenoutput_owner_public_key_status_network" to table: "token_outputs"
CREATE INDEX "tokenoutput_owner_public_key_status_network" ON "token_outputs" ("owner_public_key", "status", "network");
-- Create index "tokenoutput_token_identifier_status" to table: "token_outputs"
CREATE INDEX "tokenoutput_token_identifier_status" ON "token_outputs" ("token_identifier", "status");
