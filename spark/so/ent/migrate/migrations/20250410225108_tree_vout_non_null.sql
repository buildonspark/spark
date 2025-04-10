-- Backfill "vout" column in "trees" table
UPDATE "trees" SET vout = tree_nodes.vout FROM "tree_nodes" WHERE trees.id = tree_nodes.tree_node_tree AND tree_nodes.tree_node_parent IS NULL;

-- Modify "trees" table
ALTER TABLE "trees" ALTER COLUMN "vout" SET NOT NULL;
