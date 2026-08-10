-- atlas:txmode none

-- Create index "treenode_status_network_update_time_create_time" to table: "tree_nodes"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "treenode_status_network_update_time_create_time" ON "tree_nodes" ("status", "network", "update_time", "create_time");
-- Drop index "treenode_status_network_update_time" from table: "tree_nodes"
DROP INDEX CONCURRENTLY IF EXISTS "treenode_status_network_update_time";
