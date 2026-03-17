-- atlas:txmode none

-- Create index "utxoswap_utxo_value_sats_deposit_address_utxoswaps" to table: "utxo_swaps"
CREATE UNIQUE INDEX CONCURRENTLY "utxoswap_utxo_value_sats_deposit_address_utxoswaps" ON "utxo_swaps" ("utxo_value_sats", "deposit_address_utxoswaps") WHERE status NOT IN ('CANCELLED', 'COMPLETED');
