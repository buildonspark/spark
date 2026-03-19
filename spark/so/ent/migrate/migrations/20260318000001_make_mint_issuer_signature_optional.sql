-- Modify "token_mints" table
ALTER TABLE "token_mints" ALTER COLUMN "issuer_signature" DROP NOT NULL;
