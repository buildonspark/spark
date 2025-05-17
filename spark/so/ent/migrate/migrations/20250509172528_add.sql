-- Create index "treenode_node_confirmation_height" to table: "tree_nodes"
CREATE INDEX "treenode_node_confirmation_height" ON "tree_nodes" ("node_confirmation_height");
-- Create index "treenode_refund_confirmation_height" to table: "tree_nodes"
CREATE INDEX "treenode_refund_confirmation_height" ON "tree_nodes" ("refund_confirmation_height");
