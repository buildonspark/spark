-- atlas:txmode none

-- Create index "treenode_status_network_update_time_create_time_parent" to table: "tree_nodes"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "treenode_status_network_update_time_create_time_parent" ON "tree_nodes" ("status", "network", "update_time", "create_time", "tree_node_parent");
-- Drop index "treenode_status_network_update_time_create_time" from table: "tree_nodes"
DROP INDEX CONCURRENTLY IF EXISTS "treenode_status_network_update_time_create_time";
