-- Modify "transfer_leafs" table
ALTER TABLE "transfer_leafs" ADD COLUMN "sender_key_tweak_proof" bytea NULL, ADD COLUMN "receiver_key_tweak" bytea NULL;
