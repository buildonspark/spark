-- Drop index "partners_jwt_public_key_key" from table: "partners"
DROP INDEX "partners_jwt_public_key_key";
-- Drop index "partners_partner_id_key" from table: "partners"
DROP INDEX "partners_partner_id_key";
-- Modify "partners" table
ALTER TABLE "partners" ADD COLUMN "label" character varying NOT NULL;
-- Create index "partner_partner_id_label" to table: "partners"
CREATE UNIQUE INDEX "partner_partner_id_label" ON "partners" ("partner_id", "label");
