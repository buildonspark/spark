ALTER TABLE "utxo_swaps" DROP CONSTRAINT "utxo_swaps_utxos_utxo";
ALTER TABLE "utxo_swaps" ALTER COLUMN "utxo_swap_utxo" DROP NOT NULL;
ALTER TABLE "utxo_swaps" ADD CONSTRAINT "utxo_swaps_utxos_utxo" FOREIGN KEY ("utxo_swap_utxo") REFERENCES "utxos" ("id") ON UPDATE NO ACTION ON DELETE SET NULL NOT VALID;
