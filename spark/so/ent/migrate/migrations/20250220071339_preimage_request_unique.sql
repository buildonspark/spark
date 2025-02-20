-- Drop index "preimage_requests_payment_hash_key" from table: "preimage_requests"
DROP INDEX "preimage_requests_payment_hash_key";
-- Modify "preimage_requests" table
ALTER TABLE "preimage_requests" ADD COLUMN "receiver_identity_pubkey" bytea NULL;
-- Create index "preimagerequest_payment_hash_receiver_identity_pubkey" to table: "preimage_requests"
CREATE INDEX "preimagerequest_payment_hash_receiver_identity_pubkey" ON "preimage_requests" ("payment_hash", "receiver_identity_pubkey");
-- Modify "preimage_shares" table
ALTER TABLE "preimage_shares" ALTER COLUMN "threshold" TYPE bigint;
-- Modify "signing_keyshares" table
ALTER TABLE "signing_keyshares" ALTER COLUMN "min_signers" TYPE bigint;
-- Rename an index from "tokenfreeze_owner_public_key_token_public_key_wallet_provided_f" to "tokenfreeze_owner_public_key_t_ef6980250ce1eed77a47a185bcaa7102"
ALTER INDEX "tokenfreeze_owner_public_key_token_public_key_wallet_provided_f" RENAME TO "tokenfreeze_owner_public_key_t_ef6980250ce1eed77a47a185bcaa7102";
-- Rename an index from "tokenfreeze_owner_public_key_token_public_key_wallet_provided_t" to "tokenfreeze_owner_public_key_t_466b963c585651ef3b654fb1f5eca48a"
ALTER INDEX "tokenfreeze_owner_public_key_token_public_key_wallet_provided_t" RENAME TO "tokenfreeze_owner_public_key_t_466b963c585651ef3b654fb1f5eca48a";
-- Modify "tree_nodes" table
ALTER TABLE "tree_nodes" ALTER COLUMN "vout" TYPE integer;
-- Modify "token_leafs" table
ALTER TABLE "token_leafs" DROP CONSTRAINT "token_leafs_token_transaction_receipts_leaf_created_token_trans", DROP CONSTRAINT "token_leafs_token_transaction_receipts_leaf_spent_token_transac", ALTER COLUMN "leaf_created_transaction_output_vout" TYPE bigint, ALTER COLUMN "leaf_spent_transaction_input_vout" TYPE bigint, ADD CONSTRAINT "token_leafs_token_transaction__75c99b38b4c6cdb582c58b9435317865" FOREIGN KEY ("token_leaf_leaf_created_token_transaction_receipt") REFERENCES "token_transaction_receipts" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "token_leafs_token_transaction__bb513b59f0a301bdb1166b6a6381fe6b" FOREIGN KEY ("token_leaf_leaf_spent_token_transaction_receipt") REFERENCES "token_transaction_receipts" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
