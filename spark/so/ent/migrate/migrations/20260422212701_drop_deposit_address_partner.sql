-- atlas:nolint DS102

-- Pre-migration check: ensure table is empty before dropping
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM "deposit_address_partners" LIMIT 1) THEN
    RAISE EXCEPTION 'Cannot drop table: deposit_address_partners is not empty. Data must be migrated or removed first.';
  END IF;
END $$;

-- Drop "deposit_address_partners" table
DROP TABLE "deposit_address_partners";
