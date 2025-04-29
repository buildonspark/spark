-- Create "block_heights" table
CREATE TABLE "block_heights" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "height" bigint NOT NULL, "network" character varying NOT NULL, PRIMARY KEY ("id"));
-- Create "cooperative_exits" table
CREATE TABLE "cooperative_exits" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "exit_txid" bytea NOT NULL, "confirmation_height" bigint NULL, "cooperative_exit_transfer" uuid NOT NULL, PRIMARY KEY ("id"));
-- Create index "cooperative_exits_exit_txid_key" to table: "cooperative_exits"
CREATE UNIQUE INDEX "cooperative_exits_exit_txid_key" ON "cooperative_exits" ("exit_txid");
-- Create index "cooperativeexit_cooperative_exit_transfer" to table: "cooperative_exits"
CREATE INDEX "cooperativeexit_cooperative_exit_transfer" ON "cooperative_exits" ("cooperative_exit_transfer");
-- Create "deposit_addresses" table
CREATE TABLE "deposit_addresses" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "address" character varying NOT NULL, "owner_identity_pubkey" bytea NOT NULL, "owner_signing_pubkey" bytea NOT NULL, "confirmation_height" bigint NULL, "confirmation_txid" character varying NULL, "deposit_address_signing_keyshare" uuid NOT NULL, PRIMARY KEY ("id"));
-- Create index "deposit_addresses_address_key" to table: "deposit_addresses"
CREATE UNIQUE INDEX "deposit_addresses_address_key" ON "deposit_addresses" ("address");
-- Create index "depositaddress_address" to table: "deposit_addresses"
CREATE INDEX "depositaddress_address" ON "deposit_addresses" ("address");
-- Create index "depositaddress_owner_identity_pubkey" to table: "deposit_addresses"
CREATE INDEX "depositaddress_owner_identity_pubkey" ON "deposit_addresses" ("owner_identity_pubkey");
-- Create index "depositaddress_owner_signing_pubkey" to table: "deposit_addresses"
CREATE INDEX "depositaddress_owner_signing_pubkey" ON "deposit_addresses" ("owner_signing_pubkey");
-- Create "preimage_requests" table
CREATE TABLE "preimage_requests" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "payment_hash" bytea NOT NULL, "status" character varying NOT NULL, "receiver_identity_pubkey" bytea NULL, "preimage_request_transfers" uuid NULL, PRIMARY KEY ("id"));
-- Create index "preimagerequest_payment_hash_receiver_identity_pubkey" to table: "preimage_requests"
CREATE INDEX "preimagerequest_payment_hash_receiver_identity_pubkey" ON "preimage_requests" ("payment_hash", "receiver_identity_pubkey");
-- Create "preimage_shares" table
CREATE TABLE "preimage_shares" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "payment_hash" bytea NOT NULL, "preimage_share" bytea NOT NULL, "threshold" integer NOT NULL, "owner_identity_pubkey" bytea NOT NULL, "invoice_string" character varying NOT NULL, "preimage_request_preimage_shares" uuid NULL, PRIMARY KEY ("id"));
-- Create index "preimage_shares_payment_hash_key" to table: "preimage_shares"
CREATE UNIQUE INDEX "preimage_shares_payment_hash_key" ON "preimage_shares" ("payment_hash");
-- Create index "preimage_shares_preimage_request_preimage_shares_key" to table: "preimage_shares"
CREATE UNIQUE INDEX "preimage_shares_preimage_request_preimage_shares_key" ON "preimage_shares" ("preimage_request_preimage_shares");
-- Create index "preimageshare_payment_hash" to table: "preimage_shares"
CREATE INDEX "preimageshare_payment_hash" ON "preimage_shares" ("payment_hash");
-- Create "signing_keyshares" table
CREATE TABLE "signing_keyshares" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "status" character varying NOT NULL, "secret_share" bytea NOT NULL, "public_shares" jsonb NOT NULL, "public_key" bytea NOT NULL, "min_signers" integer NOT NULL, "coordinator_index" bigint NOT NULL, PRIMARY KEY ("id"));
-- Create index "signing_keyshares_public_key_key" to table: "signing_keyshares"
CREATE UNIQUE INDEX "signing_keyshares_public_key_key" ON "signing_keyshares" ("public_key");
-- Create index "signingkeyshare_coordinator_index" to table: "signing_keyshares"
CREATE INDEX "signingkeyshare_coordinator_index" ON "signing_keyshares" ("coordinator_index");
-- Create "signing_nonces" table
CREATE TABLE "signing_nonces" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "nonce" bytea NOT NULL, "nonce_commitment" bytea NOT NULL, PRIMARY KEY ("id"));
-- Create index "signingnonce_nonce_commitment" to table: "signing_nonces"
CREATE INDEX "signingnonce_nonce_commitment" ON "signing_nonces" ("nonce_commitment");
-- Create "token_freezes" table
CREATE TABLE "token_freezes" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "status" character varying NOT NULL, "owner_public_key" bytea NOT NULL, "token_public_key" bytea NOT NULL, "issuer_signature" bytea NOT NULL, "wallet_provided_freeze_timestamp" bigint NOT NULL, "wallet_provided_thaw_timestamp" bigint NULL, PRIMARY KEY ("id"));
-- Create index "token_freezes_issuer_signature_key" to table: "token_freezes"
CREATE UNIQUE INDEX "token_freezes_issuer_signature_key" ON "token_freezes" ("issuer_signature");
-- Create index "tokenfreeze_owner_public_key_token_public_key_wallet_provided_f" to table: "token_freezes"
CREATE UNIQUE INDEX "tokenfreeze_owner_public_key_token_public_key_wallet_provided_f" ON "token_freezes" ("owner_public_key", "token_public_key", "wallet_provided_freeze_timestamp");
-- Create index "tokenfreeze_owner_public_key_token_public_key_wallet_provided_t" to table: "token_freezes"
CREATE UNIQUE INDEX "tokenfreeze_owner_public_key_token_public_key_wallet_provided_t" ON "token_freezes" ("owner_public_key", "token_public_key", "wallet_provided_thaw_timestamp");
-- Create "token_leafs" table
CREATE TABLE "token_leafs" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "status" character varying NOT NULL, "owner_public_key" bytea NOT NULL, "withdraw_bond_sats" bigint NOT NULL, "withdraw_relative_block_locktime" bigint NOT NULL, "withdraw_revocation_public_key" bytea NOT NULL, "token_public_key" bytea NOT NULL, "token_amount" bytea NOT NULL, "leaf_created_transaction_output_vout" integer NOT NULL, "leaf_spent_ownership_signature" bytea NULL, "leaf_spent_operator_specific_ownership_signature" bytea NULL, "leaf_spent_transaction_input_vout" integer NULL, "leaf_spent_revocation_private_key" bytea NULL, "token_leaf_revocation_keyshare" uuid NOT NULL, "token_leaf_leaf_created_token_transaction_receipt" uuid NULL, "token_leaf_leaf_spent_token_transaction_receipt" uuid NULL, PRIMARY KEY ("id"));
-- Create index "tokenleaf_owner_public_key_token_public_key" to table: "token_leafs"
CREATE INDEX "tokenleaf_owner_public_key_token_public_key" ON "token_leafs" ("owner_public_key", "token_public_key");
-- Create "token_mints" table
CREATE TABLE "token_mints" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "issuer_public_key" bytea NOT NULL, "wallet_provided_timestamp" bigint NOT NULL, "issuer_signature" bytea NOT NULL, "operator_specific_issuer_signature" bytea NULL, PRIMARY KEY ("id"));
-- Create index "token_mints_issuer_signature_key" to table: "token_mints"
CREATE UNIQUE INDEX "token_mints_issuer_signature_key" ON "token_mints" ("issuer_signature");
-- Create index "token_mints_operator_specific_issuer_signature_key" to table: "token_mints"
CREATE UNIQUE INDEX "token_mints_operator_specific_issuer_signature_key" ON "token_mints" ("operator_specific_issuer_signature");
-- Create index "token_mints_wallet_provided_timestamp_key" to table: "token_mints"
CREATE UNIQUE INDEX "token_mints_wallet_provided_timestamp_key" ON "token_mints" ("wallet_provided_timestamp");
-- Create "token_transaction_receipts" table
CREATE TABLE "token_transaction_receipts" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "partial_token_transaction_hash" bytea NOT NULL, "finalized_token_transaction_hash" bytea NOT NULL, "operator_signature" bytea NULL, "token_transaction_receipt_mint" uuid NULL, PRIMARY KEY ("id"));
-- Create index "token_transaction_receipts_finalized_token_transaction_hash_key" to table: "token_transaction_receipts"
CREATE UNIQUE INDEX "token_transaction_receipts_finalized_token_transaction_hash_key" ON "token_transaction_receipts" ("finalized_token_transaction_hash");
-- Create index "token_transaction_receipts_operator_signature_key" to table: "token_transaction_receipts"
CREATE UNIQUE INDEX "token_transaction_receipts_operator_signature_key" ON "token_transaction_receipts" ("operator_signature");
-- Create index "tokentransactionreceipt_finalized_token_transaction_hash" to table: "token_transaction_receipts"
CREATE INDEX "tokentransactionreceipt_finalized_token_transaction_hash" ON "token_transaction_receipts" ("finalized_token_transaction_hash");
-- Create "transfer_leafs" table
CREATE TABLE "transfer_leafs" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "secret_cipher" bytea NULL, "signature" bytea NULL, "previous_refund_tx" bytea NOT NULL, "intermediate_refund_tx" bytea NOT NULL, "key_tweak" bytea NULL, "transfer_leaf_transfer" uuid NOT NULL, "transfer_leaf_leaf" uuid NOT NULL, PRIMARY KEY ("id"));
-- Create index "transferleaf_transfer_leaf_leaf" to table: "transfer_leafs"
CREATE INDEX "transferleaf_transfer_leaf_leaf" ON "transfer_leafs" ("transfer_leaf_leaf");
-- Create index "transferleaf_transfer_leaf_transfer" to table: "transfer_leafs"
CREATE INDEX "transferleaf_transfer_leaf_transfer" ON "transfer_leafs" ("transfer_leaf_transfer");
-- Create "transfers" table
CREATE TABLE "transfers" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "sender_identity_pubkey" bytea NOT NULL, "receiver_identity_pubkey" bytea NOT NULL, "total_value" bigint NOT NULL, "status" character varying NOT NULL, "type" character varying NOT NULL, "expiry_time" timestamptz NOT NULL, "completion_time" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "transfer_receiver_identity_pubkey" to table: "transfers"
CREATE INDEX "transfer_receiver_identity_pubkey" ON "transfers" ("receiver_identity_pubkey");
-- Create index "transfer_sender_identity_pubkey" to table: "transfers"
CREATE INDEX "transfer_sender_identity_pubkey" ON "transfers" ("sender_identity_pubkey");
-- Create index "transfer_status" to table: "transfers"
CREATE INDEX "transfer_status" ON "transfers" ("status");
-- Create "tree_nodes" table
CREATE TABLE "tree_nodes" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "value" bigint NOT NULL, "status" character varying NOT NULL, "verifying_pubkey" bytea NOT NULL, "owner_identity_pubkey" bytea NOT NULL, "owner_signing_pubkey" bytea NOT NULL, "raw_tx" bytea NOT NULL, "vout" smallint NOT NULL, "raw_refund_tx" bytea NULL, "tree_node_tree" uuid NOT NULL, "tree_node_parent" uuid NULL, "tree_node_signing_keyshare" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "tree_nodes_tree_nodes_parent" FOREIGN KEY ("tree_node_parent") REFERENCES "tree_nodes" ("id") ON UPDATE NO ACTION ON DELETE SET NULL);
-- Create index "treenode_owner_identity_pubkey" to table: "tree_nodes"
CREATE INDEX "treenode_owner_identity_pubkey" ON "tree_nodes" ("owner_identity_pubkey");
-- Create index "treenode_tree_node_parent" to table: "tree_nodes"
CREATE INDEX "treenode_tree_node_parent" ON "tree_nodes" ("tree_node_parent");
-- Create index "treenode_tree_node_tree" to table: "tree_nodes"
CREATE INDEX "treenode_tree_node_tree" ON "tree_nodes" ("tree_node_tree");
-- Create "trees" table
CREATE TABLE "trees" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "owner_identity_pubkey" bytea NOT NULL, "status" character varying NOT NULL, "network" character varying NOT NULL, "base_txid" bytea NULL, "tree_root" uuid NULL, PRIMARY KEY ("id"));
-- Create "user_signed_transactions" table
CREATE TABLE "user_signed_transactions" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "transaction" bytea NOT NULL, "user_signature" bytea NOT NULL, "signing_commitments" bytea NOT NULL, "user_signature_commitment" bytea NOT NULL, "user_signed_transaction_tree_node" uuid NOT NULL, "user_signed_transaction_preimage_request" uuid NOT NULL, PRIMARY KEY ("id"));
-- Modify "cooperative_exits" table
ALTER TABLE "cooperative_exits" ADD CONSTRAINT "cooperative_exits_transfers_transfer" FOREIGN KEY ("cooperative_exit_transfer") REFERENCES "transfers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Modify "deposit_addresses" table
ALTER TABLE "deposit_addresses" ADD CONSTRAINT "deposit_addresses_signing_keyshares_signing_keyshare" FOREIGN KEY ("deposit_address_signing_keyshare") REFERENCES "signing_keyshares" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Modify "preimage_requests" table
ALTER TABLE "preimage_requests" ADD CONSTRAINT "preimage_requests_transfers_transfers" FOREIGN KEY ("preimage_request_transfers") REFERENCES "transfers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "preimage_shares" table
ALTER TABLE "preimage_shares" ADD CONSTRAINT "preimage_shares_preimage_requests_preimage_shares" FOREIGN KEY ("preimage_request_preimage_shares") REFERENCES "preimage_requests" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "token_leafs" table
ALTER TABLE "token_leafs" ADD CONSTRAINT "token_leafs_signing_keyshares_revocation_keyshare" FOREIGN KEY ("token_leaf_revocation_keyshare") REFERENCES "signing_keyshares" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, ADD CONSTRAINT "token_leafs_token_transaction__75c99b38b4c6cdb582c58b9435317865" FOREIGN KEY ("token_leaf_leaf_created_token_transaction_receipt") REFERENCES "token_transaction_receipts" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "token_leafs_token_transaction__bb513b59f0a301bdb1166b6a6381fe6b" FOREIGN KEY ("token_leaf_leaf_spent_token_transaction_receipt") REFERENCES "token_transaction_receipts" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "token_transaction_receipts" table
ALTER TABLE "token_transaction_receipts" ADD CONSTRAINT "token_transaction_receipts_token_mints_mint" FOREIGN KEY ("token_transaction_receipt_mint") REFERENCES "token_mints" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "transfer_leafs" table
ALTER TABLE "transfer_leafs" ADD CONSTRAINT "transfer_leafs_transfers_transfer" FOREIGN KEY ("transfer_leaf_transfer") REFERENCES "transfers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, ADD CONSTRAINT "transfer_leafs_tree_nodes_leaf" FOREIGN KEY ("transfer_leaf_leaf") REFERENCES "tree_nodes" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Modify "tree_nodes" table
ALTER TABLE "tree_nodes" ADD CONSTRAINT "tree_nodes_signing_keyshares_signing_keyshare" FOREIGN KEY ("tree_node_signing_keyshare") REFERENCES "signing_keyshares" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, ADD CONSTRAINT "tree_nodes_trees_tree" FOREIGN KEY ("tree_node_tree") REFERENCES "trees" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Modify "trees" table
ALTER TABLE "trees" ADD CONSTRAINT "trees_tree_nodes_root" FOREIGN KEY ("tree_root") REFERENCES "tree_nodes" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "user_signed_transactions" table
ALTER TABLE "user_signed_transactions" ADD CONSTRAINT "user_signed_transactions_preimage_requests_preimage_request" FOREIGN KEY ("user_signed_transaction_preimage_request") REFERENCES "preimage_requests" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, ADD CONSTRAINT "user_signed_transactions_tree_nodes_tree_node" FOREIGN KEY ("user_signed_transaction_tree_node") REFERENCES "tree_nodes" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
