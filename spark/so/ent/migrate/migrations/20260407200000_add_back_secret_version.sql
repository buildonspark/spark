-- Modify "signing_keyshares" table
ALTER TABLE "signing_keyshares" ADD COLUMN IF NOT EXISTS "secret_version" integer NULL;
