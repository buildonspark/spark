-- Modify "token_leafs" table
ALTER TABLE "token_leafs" ADD COLUMN "confirmed_withdraw_block_hash" bytea NULL;
-- Create index "tokenleaf_confirmed_withdraw_block_hash" to table: "token_leafs"
CREATE INDEX "tokenleaf_confirmed_withdraw_block_hash" ON "token_leafs" ("confirmed_withdraw_block_hash");
