-- Modify "tree_nodes" table
ALTER TABLE "tree_nodes" 
    ADD COLUMN "confirmation_height" bigint NOT NULL DEFAULT 0; 