-- Drop index "treenode_refund_confirmation_height" from table: "tree_nodes"
DROP INDEX "treenode_refund_confirmation_height";
-- Create index "treenode_refund_confirmation_h_7661a1b6fbbfab014f2fe797e6f6f92a" to table: "tree_nodes"
CREATE INDEX IF NOT EXISTS "treenode_refund_confirmation_h_7661a1b6fbbfab014f2fe797e6f6f92a" ON "tree_nodes" ("refund_confirmation_height", "node_confirmation_height", "network");
