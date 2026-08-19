-- Modify "token_allowances" table. The column is nullable, so the ADD COLUMN is
-- metadata-only in Postgres 11+ and does not rewrite the table.
ALTER TABLE "token_allowances" ADD COLUMN "revoke_version" bigint NULL;
