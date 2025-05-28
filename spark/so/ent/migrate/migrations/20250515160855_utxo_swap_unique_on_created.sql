-- Drop index "utxoswap_utxo_swap_utxo" from table: "utxo_swaps"
DROP INDEX "utxoswap_utxo_swap_utxo";
-- Create index "utxoswap_utxo_swap_utxo" to table: "utxo_swaps"
CREATE UNIQUE INDEX "utxoswap_utxo_swap_utxo" ON "utxo_swaps" ("utxo_swap_utxo") WHERE ((status)::text = 'CREATED'::text);
