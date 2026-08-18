-- Create "token_allowances" table
CREATE TABLE "token_allowances" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "allowance_id" uuid NOT NULL,
  "status" character varying NOT NULL DEFAULT 'ACTIVE',
  "owner_public_key" bytea NOT NULL,
  "spender_public_key" bytea NOT NULL,
  "token_identifier" bytea NOT NULL,
  "per_transaction_cap" bytea NOT NULL,
  "total_limit" bytea NOT NULL,
  "spent_amount" bytea NOT NULL,
  "recipient_allowlist" jsonb NULL,
  "expiry_time" timestamptz NOT NULL,
  "network" character varying NOT NULL,
  "owner_signature" bytea NOT NULL,
  "statement_hash" bytea NOT NULL,
  "version" bigint NOT NULL,
  "owner_provided_timestamp" bigint NOT NULL,
  "owner_provided_revoke_timestamp" bigint NULL,
  "revoke_signature" bytea NULL,
  "token_create_id" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "token_allowances_token_creates_token_allowance" FOREIGN KEY ("token_create_id") REFERENCES "token_creates" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "token_allowances_allowance_id_key" to table: "token_allowances"
CREATE UNIQUE INDEX "token_allowances_allowance_id_key" ON "token_allowances" ("allowance_id");
-- Create index "token_allowances_owner_signature_key" to table: "token_allowances"
CREATE UNIQUE INDEX "token_allowances_owner_signature_key" ON "token_allowances" ("owner_signature");
-- Create index "tokenallowance_owner_public_key_status" to table: "token_allowances"
CREATE INDEX "tokenallowance_owner_public_key_status" ON "token_allowances" ("owner_public_key", "status");
-- Create index "tokenallowance_spender_public_key_status" to table: "token_allowances"
CREATE INDEX "tokenallowance_spender_public_key_status" ON "token_allowances" ("spender_public_key", "status");
-- Create index "tokenallowance_unique_active_grant" to table: "token_allowances"
CREATE UNIQUE INDEX "tokenallowance_unique_active_grant" ON "token_allowances" ("owner_public_key", "spender_public_key", "token_create_id") WHERE ((status)::text = 'ACTIVE'::text);
