-- We have to do a bunch of deletions to get the unique constraint to work. To be safe, only do this
-- with REGTEST. If we need to do this in other networks, it should be done manually with great care!
CREATE TEMP TABLE trees_to_delete AS (
    SELECT id FROM trees WHERE (base_txid, vout) IN (
        SELECT base_txid, vout FROM trees WHERE network = 'REGTEST' GROUP BY base_txid, vout HAVING COUNT(*) > 1
    )
);

CREATE TEMP TABLE tree_nodes_to_delete AS (
    SELECT id FROM tree_nodes WHERE tree_node_tree in (
        SELECT id FROM trees_to_delete
    )
);

-- ------------------------------------
-- User Signed Transactions / Preimages
-- ------------------------------------
CREATE TEMP TABLE user_signed_transactions_to_delete AS (
    SELECT id, user_signed_transaction_preimage_request AS preimage_request_id FROM user_signed_transactions WHERE user_signed_transaction_tree_node in (
        SELECT id FROM tree_nodes_to_delete
    )
);

CREATE TEMP TABLE preimage_requests_to_delete AS (
    SELECT DISTINCT preimage_requests.id FROM preimage_requests WHERE NOT EXISTS (
        SELECT 1 FROM user_signed_transactions WHERE user_signed_transactions.user_signed_transaction_preimage_request = preimage_requests.id AND user_signed_transactions.id NOT IN (
            SELECT id FROM user_signed_transactions_to_delete
        )
    )
);

-- Delete all of the preimage shares that reference the preimage requests.
DELETE FROM preimage_shares where preimage_request_preimage_shares IN (
    SELECT id FROM preimage_requests_to_delete
);

-- Delete all of the user signed transactions referenced by tree nodes.
DELETE FROM user_signed_transactions WHERE id IN (
    SELECT id FROM user_signed_transactions_to_delete
);

-- Delete all of the preimage requests referenced by the user signed transactions.
DELETE FROM preimage_requests WHERE id IN (
    SELECT id FROM preimage_requests_to_delete
);

-- -----------------------------
-- Transfers / Cooperative Exits
-- -----------------------------
CREATE TEMP TABLE transfer_leafs_to_delete AS (
    SELECT id, transfer_leaf_transfer as transfer_id FROM transfer_leafs WHERE transfer_leaf_leaf IN (
        SELECT id FROM tree_nodes_to_delete
    )
);

CREATE TEMP TABLE transfers_to_delete AS (
    SELECT transfer_leaf_transfer as id FROM transfer_leafs GROUP BY transfer_leafs.transfer_leaf_transfer HAVING EVERY(transfer_leaf_leaf IN (SELECT id FROM tree_nodes_to_delete))
);

-- Delete cooperative exits that reference the transfers that will be deleted
DELETE FROM cooperative_exits WHERE cooperative_exit_transfer IN (
    SELECT id FROM transfers_to_delete
);

-- Delete the transfer leafs that reference the transfers that will be deleted.
DELETE FROM transfer_leafs WHERE id IN (
    SELECT id FROM transfer_leafs_to_delete
);

-- Delete the transfers.
DELETE FROM transfers WHERE id IN (
    SELECT id FROM transfers_to_delete
);

-- ---------------------------
-- Cleanup
-- ---------------------------

-- Delete all of the tree nodes.
DELETE FROM tree_nodes WHERE id IN (
    SELECT id FROM tree_nodes_to_delete
);

-- Delete all of the trees.
DELETE FROM trees WHERE id IN (
    SELECT id FROM trees_to_delete
);

-- Create index "tree_base_txid_vout" to table: "trees"
CREATE UNIQUE INDEX "tree_base_txid_vout" ON "trees" ("base_txid", "vout");
