-- Backfill utxo_value_sats from utxos.amount
UPDATE "utxo_swaps" 
SET utxo_value_sats = utxos.amount 
FROM "utxos" 
WHERE utxo_swaps.utxo_swap_utxo = utxos.id 
  AND utxo_swaps.utxo_value_sats IS NULL;

