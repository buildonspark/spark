-- Modify "l1token_creates" table
ALTER TABLE "l1token_creates" ADD COLUMN "extra_metadata" bytea NULL;
-- Modify "token_creates" table
ALTER TABLE "token_creates" ADD COLUMN "extra_metadata" bytea NULL;
