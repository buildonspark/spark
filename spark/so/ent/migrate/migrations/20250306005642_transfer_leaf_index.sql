-- Modify "preimage_shares" table
ALTER TABLE "preimage_shares" ALTER COLUMN "threshold" TYPE bigint;
-- Modify "signing_keyshares" table
ALTER TABLE "signing_keyshares" ALTER COLUMN "min_signers" TYPE bigint;
-- Drop index "tokenfreeze_owner_public_key_token_public_key_wallet_provided_f" from table: "token_freezes"
DROP INDEX "tokenfreeze_owner_public_key_token_public_key_wallet_provided_f";
-- Drop index "tokenfreeze_owner_public_key_token_public_key_wallet_provided_t" from table: "token_freezes"
DROP INDEX "tokenfreeze_owner_public_key_token_public_key_wallet_provided_t";
-- Create index "tokenfreeze_owner_public_key_t_466b963c585651ef3b654fb1f5eca48a" to table: "token_freezes"
CREATE UNIQUE INDEX "tokenfreeze_owner_public_key_t_466b963c585651ef3b654fb1f5eca48a" ON "token_freezes" ("owner_public_key", "token_public_key", "wallet_provided_thaw_timestamp");
-- Create index "tokenfreeze_owner_public_key_t_ef6980250ce1eed77a47a185bcaa7102" to table: "token_freezes"
CREATE UNIQUE INDEX "tokenfreeze_owner_public_key_t_ef6980250ce1eed77a47a185bcaa7102" ON "token_freezes" ("owner_public_key", "token_public_key", "wallet_provided_freeze_timestamp");
-- Modify "token_transaction_receipts" table
ALTER TABLE "token_transaction_receipts" ADD COLUMN "status" character varying NOT NULL;
-- Create index "transfer_update_time" to table: "transfers"
CREATE INDEX "transfer_update_time" ON "transfers" ("update_time");
-- Modify "tree_nodes" table
ALTER TABLE "tree_nodes" ALTER COLUMN "vout" TYPE integer;
-- Modify "token_leafs" table
ALTER TABLE "token_leafs" DROP CONSTRAINT "token_leafs_token_transaction_receipts_leaf_created_token_trans", DROP CONSTRAINT "token_leafs_token_transaction_receipts_leaf_spent_token_transac", ALTER COLUMN "leaf_created_transaction_output_vout" TYPE bigint, ALTER COLUMN "leaf_spent_transaction_input_vout" TYPE bigint, ADD CONSTRAINT "token_leafs_token_transaction__75c99b38b4c6cdb582c58b9435317865" FOREIGN KEY ("token_leaf_leaf_created_token_transaction_receipt") REFERENCES "token_transaction_receipts" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "token_leafs_token_transaction__bb513b59f0a301bdb1166b6a6381fe6b" FOREIGN KEY ("token_leaf_leaf_spent_token_transaction_receipt") REFERENCES "token_transaction_receipts" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
