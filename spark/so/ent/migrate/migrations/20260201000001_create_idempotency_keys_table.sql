-- Create "idempotency_keys" table
CREATE TABLE "idempotency_keys" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "idempotency_key" character varying NOT NULL, "method_name" character varying NOT NULL, "response" jsonb NULL, PRIMARY KEY ("id"));
-- Create index "idempotency_keys_create_time" to table: "idempotency_keys"
CREATE INDEX "idempotency_keys_create_time" ON "idempotency_keys" ("create_time");
-- Create index "idempotency_keys_idempotency_key_method_name" to table: "idempotency_keys"
CREATE UNIQUE INDEX "idempotency_keys_idempotency_key_method_name" ON "idempotency_keys" ("idempotency_key", "method_name");
