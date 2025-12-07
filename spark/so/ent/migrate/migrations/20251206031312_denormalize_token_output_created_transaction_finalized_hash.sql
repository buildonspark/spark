-- Modify "token_outputs" table
ALTER TABLE "token_outputs" ADD COLUMN "created_transaction_finalized_hash" bytea NULL;
-- Create index "tokenoutput_created_transactio_58631bd619900f490dad4c2fad29965a" to table: "token_outputs"
CREATE UNIQUE INDEX "tokenoutput_created_transactio_58631bd619900f490dad4c2fad29965a" ON "token_outputs" ("created_transaction_finalized_hash", "created_transaction_output_vout");
