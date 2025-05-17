-- Modify "tree_nodes" table
ALTER TABLE "tree_nodes" ALTER COLUMN "node_confirmation_height" DROP NOT NULL, ALTER COLUMN "node_confirmation_height" DROP DEFAULT, ALTER COLUMN "refund_confirmation_height" DROP NOT NULL, ALTER COLUMN "refund_confirmation_height" DROP DEFAULT;
