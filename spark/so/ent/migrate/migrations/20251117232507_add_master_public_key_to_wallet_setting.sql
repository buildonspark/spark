-- Modify "wallet_settings" table
ALTER TABLE "wallet_settings" ADD COLUMN "master_identity_public_key" bytea NULL;
-- Create index "walletsetting_master_identity_public_key" to table: "wallet_settings"
CREATE INDEX "walletsetting_master_identity_public_key" ON "wallet_settings" ("master_identity_public_key");
