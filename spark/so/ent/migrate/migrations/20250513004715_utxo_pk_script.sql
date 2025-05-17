-- Modify "utxos" table
ALTER TABLE "utxos" ADD COLUMN "pk_script" bytea NOT NULL;
