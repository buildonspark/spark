-- atlas:nolint DS103

-- Pre-migration check: ensure column is NULL before dropping
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM "token_outputs" WHERE "confirmed_withdraw_block_hash" IS NOT NULL LIMIT 1) THEN
    RAISE EXCEPTION 'Cannot drop column: confirmed_withdraw_block_hash contains non-NULL values. Data must be migrated first.';
  END IF;
END $$;

-- Modify "token_outputs" table
ALTER TABLE "token_outputs" DROP COLUMN "confirmed_withdraw_block_hash";
