-- Rename a column from "confirmation_height" to "node_confirmation_height"
ALTER TABLE "tree_nodes" RENAME COLUMN "confirmation_height" TO "node_confirmation_height";

-- Add refund_confirmation_height column
ALTER TABLE "tree_nodes" ADD COLUMN "refund_confirmation_height" bigint NOT NULL DEFAULT 0;
