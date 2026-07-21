-- atlas:txmode none

-- Create index "treenode_status_network_update_time" to table: "tree_nodes"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "treenode_status_network_update_time" ON "tree_nodes" ("status", "network", "update_time");
