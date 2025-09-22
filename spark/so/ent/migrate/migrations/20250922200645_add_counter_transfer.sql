-- Modify "transfers" table
ALTER TABLE "transfers" ADD COLUMN "transfer_primary_swap_transfer" uuid NULL, ADD CONSTRAINT "transfers_transfers_primary_swap_transfer" FOREIGN KEY ("transfer_primary_swap_transfer") REFERENCES "transfers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
