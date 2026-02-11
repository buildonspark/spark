-- Modify "token_freezes" table
ALTER TABLE "token_freezes" ALTER COLUMN "owner_public_key" DROP NOT NULL;
-- Create index "tokenfreeze_token_create_id_status" to table: "token_freezes"
CREATE INDEX "tokenfreeze_token_create_id_status" ON "token_freezes" ("token_create_id", "status");
-- Create index "tokenfreeze_unique_active_global_pause" to table: "token_freezes"
CREATE UNIQUE INDEX "tokenfreeze_unique_active_global_pause" ON "token_freezes" ("token_create_id") WHERE ((owner_public_key IS NULL) AND ((status)::text = 'FROZEN'::text));
-- Create index "tokenfreeze_unique_active_per_owner_freeze" to table: "token_freezes"
CREATE UNIQUE INDEX "tokenfreeze_unique_active_per_owner_freeze" ON "token_freezes" ("owner_public_key", "token_create_id") WHERE ((owner_public_key IS NOT NULL) AND ((status)::text = 'FROZEN'::text));
