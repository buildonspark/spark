-- atlas:txmode none

-- Create index "signing_keyshares_update_time_id_idx" to table: "signing_keyshares"
CREATE INDEX CONCURRENTLY "signing_keyshares_update_time_id_idx" ON "signing_keyshares" ("update_time", "id");
-- Create index "token_outputs_update_time_id_idx" to table: "token_outputs"
CREATE INDEX CONCURRENTLY "token_outputs_update_time_id_idx" ON "token_outputs" ("update_time", "id");
-- Create index "token_partial_revocation_secret_shares_update_time_id_idx" to table: "token_partial_revocation_secret_shares"
CREATE INDEX CONCURRENTLY "token_partial_revocation_secret_shares_update_time_id_idx" ON "token_partial_revocation_secret_shares" ("update_time", "id");
-- Create index "transfer_leafs_update_time_id_idx" to table: "transfer_leafs"
CREATE INDEX CONCURRENTLY "transfer_leafs_update_time_id_idx" ON "transfer_leafs" ("update_time", "id");
