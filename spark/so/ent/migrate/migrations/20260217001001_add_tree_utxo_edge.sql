-- Modify "utxos" table
-- Step 1: Add columns (ACCESS EXCLUSIVE for milliseconds — metadata only)
ALTER TABLE "utxos" ADD COLUMN "availability_confirmed_at" timestamptz NULL, ADD COLUMN "tree_utxos" uuid NULL;

-- Step 2: Add constraint without validation (ACCESS EXCLUSIVE for milliseconds — metadata only)
ALTER TABLE "utxos"
    ADD CONSTRAINT "utxos_trees_utxos"
        FOREIGN KEY ("tree_utxos") REFERENCES "trees" ("id")
        ON UPDATE NO ACTION ON DELETE SET NULL
        NOT VALID;

-- Step 3: Validate constraint (SHARE UPDATE EXCLUSIVE — allows concurrent reads and writes)
ALTER TABLE "utxos"
    VALIDATE CONSTRAINT "utxos_trees_utxos";
