-- Backfill tree_nodes.network from trees.network (NULLs only)
UPDATE tree_nodes tn
SET network = t.network
FROM trees t
WHERE tn.tree_node_tree = t.id
  AND tn.network IS NULL;
