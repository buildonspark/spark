-- Make partner_partner_key NOT NULL (backfill is complete).
ALTER TABLE "partners" ALTER COLUMN "partner_partner_key" SET NOT NULL;

-- Change FK from ON DELETE SET NULL to ON DELETE NO ACTION (column is now required).
ALTER TABLE "partners" DROP CONSTRAINT "partners_partner_keys_partner_key",
    ADD CONSTRAINT "partners_partner_keys_partner_key"
    FOREIGN KEY ("partner_partner_key") REFERENCES "partner_keys" ("id")
    ON UPDATE NO ACTION ON DELETE NO ACTION;

-- Drop old unique index (partner_id is becoming nullable).
DROP INDEX "partner_partner_id_label";

-- Make deprecated fields nullable.
ALTER TABLE "partners" ALTER COLUMN "partner_id" DROP NOT NULL,
    ALTER COLUMN "partner_name" DROP NOT NULL,
    ALTER COLUMN "jwt_public_key" DROP NOT NULL;
