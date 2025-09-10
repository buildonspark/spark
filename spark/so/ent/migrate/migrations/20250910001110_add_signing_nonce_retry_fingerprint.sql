-- Modify "signing_nonces" table
ALTER TABLE "signing_nonces" ADD COLUMN "retry_fingerprint" bytea NULL;
