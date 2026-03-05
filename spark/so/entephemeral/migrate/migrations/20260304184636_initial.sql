-- Create "signing_keyshare_secrets" table
CREATE TABLE "signing_keyshare_secrets" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "signing_keyshare_id" uuid NOT NULL,
  "version" integer NOT NULL,
  "secret_share" bytea NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "signingkeysharesecret_signing_keyshare_id_version" to table: "signing_keyshare_secrets"
CREATE UNIQUE INDEX "signingkeysharesecret_signing_keyshare_id_version" ON "signing_keyshare_secrets" ("signing_keyshare_id", "version");
