-- Modify "utxo_swaps" table
ALTER TABLE "utxo_swaps" ADD COLUMN "consensus_managed" boolean NOT NULL DEFAULT false;
