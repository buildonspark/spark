-- Create "wallet_settings" table
CREATE TABLE "wallet_settings" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "owner_identity_public_key" bytea NOT NULL, "private_enabled" boolean NOT NULL DEFAULT false, PRIMARY KEY ("id"));
-- Create index "wallet_settings_owner_identity_public_key_key" to table: "wallet_settings"
CREATE UNIQUE INDEX "wallet_settings_owner_identity_public_key_key" ON "wallet_settings" ("owner_identity_public_key");
-- Create index "walletsetting_owner_identity_public_key" to table: "wallet_settings"
CREATE INDEX "walletsetting_owner_identity_public_key" ON "wallet_settings" ("owner_identity_public_key");
