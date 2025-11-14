-- atlas:txmode none

-- Create index "transfer_network" to table: "transfers"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "transfer_network" ON "transfers" ("network");
-- Create index "treenode_network" to table: "tree_nodes"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "treenode_network" ON "tree_nodes" ("network");
