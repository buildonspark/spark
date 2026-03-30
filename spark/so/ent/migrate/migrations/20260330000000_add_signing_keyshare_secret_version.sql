-- Add active secret version pointer for signing keyshares.
ALTER TABLE "signing_keyshares"
    ADD COLUMN "secret_version" integer NULL;

-- Make secret_share nullable for migration to ephemeral secret storage.
ALTER TABLE "signing_keyshares"
    ALTER COLUMN "secret_share" DROP NOT NULL;
