-- Modify "trees" table
ALTER TABLE "trees" ALTER COLUMN "base_txid" SET NOT NULL;
-- Create index "trees_base_txid_key" to table: "trees"
CREATE UNIQUE INDEX "trees_base_txid_key" ON "trees" ("base_txid");
