-- Modify "tree_nodes" table
ALTER TABLE "tree_nodes" ADD COLUMN "raw_refund_txid" bytea NULL, ADD COLUMN "direct_refund_txid" bytea NULL, ADD COLUMN "raw_txid" bytea NULL, ADD COLUMN "direct_txid" bytea NULL, ADD COLUMN "direct_from_cpfp_refund_txid" bytea NULL;
