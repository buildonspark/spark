-- Create "partners" table
CREATE TABLE "partners" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "partner_id" character varying NOT NULL, "partner_name" character varying NOT NULL, "jwt_public_key" bytea NOT NULL, PRIMARY KEY ("id"));
-- Create index "partners_jwt_public_key_key" to table: "partners"
CREATE UNIQUE INDEX "partners_jwt_public_key_key" ON "partners" ("jwt_public_key");
-- Create index "partners_partner_id_key" to table: "partners"
CREATE UNIQUE INDEX "partners_partner_id_key" ON "partners" ("partner_id");
