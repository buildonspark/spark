-- Modify "partner_keys" table — additive nullable column (no rewrite on PG 11+).
ALTER TABLE "partner_keys" ADD COLUMN "basic_auth_secret_hash" character varying NULL;
