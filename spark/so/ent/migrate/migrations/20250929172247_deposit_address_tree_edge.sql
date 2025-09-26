-- Modify "trees" table
ALTER TABLE "trees" ADD COLUMN "deposit_address_tree" uuid NULL, ADD CONSTRAINT "trees_deposit_addresses_tree" FOREIGN KEY ("deposit_address_tree") REFERENCES "deposit_addresses" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "trees_deposit_address_tree_key" to table: "trees"
CREATE UNIQUE INDEX "trees_deposit_address_tree_key" ON "trees" ("deposit_address_tree");
