-- Drop index "utxoswap_utxo_swap_utxo" from table: "utxo_swaps"
DROP INDEX "utxoswap_utxo_swap_utxo";
-- Modify "utxo_swaps" table
ALTER TABLE "utxo_swaps" ADD COLUMN "coordinator_identity_public_key" bytea NOT NULL;
-- Create index "utxoswap_utxo_swap_utxo" to table: "utxo_swaps"
CREATE UNIQUE INDEX "utxoswap_utxo_swap_utxo" ON "utxo_swaps" ("utxo_swap_utxo") WHERE ((status)::text <> 'CANCELLED'::text);
