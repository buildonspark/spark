-- Rename a column from "transfer_primary_swap_transfer" to "transfer_counter_swap_transfer"
ALTER TABLE "transfers" RENAME COLUMN "transfer_primary_swap_transfer" TO "transfer_counter_swap_transfer";
-- Modify "transfers" table
ALTER TABLE "transfers" DROP CONSTRAINT "transfers_transfers_primary_swap_transfer", ADD CONSTRAINT "transfers_transfers_counter_swap_transfer" FOREIGN KEY ("transfer_counter_swap_transfer") REFERENCES "transfers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
