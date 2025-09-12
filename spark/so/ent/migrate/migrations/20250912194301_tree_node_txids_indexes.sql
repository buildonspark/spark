-- Create index "treenode_direct_from_cpfp_refund_txid" to table: "tree_nodes"
CREATE INDEX "treenode_direct_from_cpfp_refund_txid" ON "tree_nodes" ("direct_from_cpfp_refund_txid") WHERE (direct_from_cpfp_refund_txid IS NOT NULL);
-- Create index "treenode_direct_refund_txid" to table: "tree_nodes"
CREATE INDEX "treenode_direct_refund_txid" ON "tree_nodes" ("direct_refund_txid") WHERE (direct_refund_txid IS NOT NULL);
-- Create index "treenode_direct_txid" to table: "tree_nodes"
CREATE INDEX "treenode_direct_txid" ON "tree_nodes" ("direct_txid") WHERE (direct_txid IS NOT NULL);
-- Create index "treenode_raw_refund_txid" to table: "tree_nodes"
CREATE INDEX "treenode_raw_refund_txid" ON "tree_nodes" ("raw_refund_txid") WHERE (raw_refund_txid IS NOT NULL);
-- Create index "treenode_raw_txid" to table: "tree_nodes"
CREATE INDEX "treenode_raw_txid" ON "tree_nodes" ("raw_txid") WHERE (raw_txid IS NOT NULL);
