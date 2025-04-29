-- Create index "treenode_owner_identity_pubkey_status" to table: "tree_nodes"
CREATE INDEX "treenode_owner_identity_pubkey_status" ON "tree_nodes" ("owner_identity_pubkey", "status");
-- Create index "tree_network" to table: "trees"
CREATE INDEX "tree_network" ON "trees" ("network");
-- Create index "tree_status" to table: "trees"
CREATE INDEX "tree_status" ON "trees" ("status");
