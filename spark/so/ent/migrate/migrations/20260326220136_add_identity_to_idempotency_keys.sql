-- Drop index "idempotency_keys_idempotency_key_method_name" from table: "idempotency_keys"
DROP INDEX "idempotency_keys_idempotency_key_method_name";
-- Modify "idempotency_keys" table
ALTER TABLE "idempotency_keys" ADD COLUMN "identity_public_key" bytea NOT NULL DEFAULT '\x';
-- Create index "idempotency_keys_key_method_identity" to table: "idempotency_keys"
CREATE UNIQUE INDEX "idempotency_keys_key_method_identity" ON "idempotency_keys" ("idempotency_key", "method_name", "identity_public_key");
