-- Modify "signing_nonces" table
ALTER TABLE "signing_nonces" ADD COLUMN "message" bytea NULL;
