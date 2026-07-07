-- atlas:txmode none

-- Create index "treenode_owner_identity_pubkey_status_network_id" to table: "tree_nodes"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "treenode_owner_identity_pubkey_status_network_id" ON "tree_nodes" ("owner_identity_pubkey", "status", "network", "id");
