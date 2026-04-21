-- Create "partner_keys" table
CREATE TABLE "partner_keys" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "partner_id" character varying NOT NULL,
  "partner_name" character varying NOT NULL,
  "jwt_public_key" bytea NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "partner_keys_partner_id_key" to table: "partner_keys"
CREATE UNIQUE INDEX "partner_keys_partner_id_key" ON "partner_keys" ("partner_id");
-- Create index "partner_keys_jwt_public_key_key" to table: "partner_keys"
CREATE UNIQUE INDEX "partner_keys_jwt_public_key_key" ON "partner_keys" ("jwt_public_key");

-- Add nullable FK column to partners (backfill manually after deploy).
ALTER TABLE "partners" ADD COLUMN "partner_partner_key" uuid;

-- Add FK constraint.
ALTER TABLE "partners" ADD CONSTRAINT "partners_partner_keys_partner_key"
    FOREIGN KEY ("partner_partner_key") REFERENCES "partner_keys" ("id")
    ON UPDATE NO ACTION ON DELETE SET NULL NOT VALID;
ALTER TABLE "partners" VALIDATE CONSTRAINT "partners_partner_keys_partner_key";

-- Create new composite unique index.
CREATE UNIQUE INDEX "partner_label_partner_partner_key" ON "partners" ("label", "partner_partner_key");
