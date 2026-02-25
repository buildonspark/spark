-- Modify "utxo_swaps" table
ALTER TABLE "utxo_swaps" ADD COLUMN "requested_secondary_transfer_id" uuid NULL;
