-- Backfill "key_tweaked_height" column
UPDATE "cooperative_exits" SET "key_tweaked_height" = "confirmation_height" WHERE "confirmation_height" IS NOT NULL and "key_tweaked_height" IS NULL;
