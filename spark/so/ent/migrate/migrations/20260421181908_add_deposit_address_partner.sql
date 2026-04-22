-- Create "deposit_address_partners" table
CREATE TABLE "deposit_address_partners" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "deposit_address_partner_partner" uuid NOT NULL,
  "deposit_address_partner_deposit_address" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "deposit_address_partners_deposit_addresses_deposit_address" FOREIGN KEY ("deposit_address_partner_deposit_address") REFERENCES "deposit_addresses" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT "deposit_address_partners_partners_partner" FOREIGN KEY ("deposit_address_partner_partner") REFERENCES "partners" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "depositaddresspartner_deposit_address_partner_deposit_address" to table: "deposit_address_partners"
CREATE UNIQUE INDEX "depositaddresspartner_deposit_address_partner_deposit_address" ON "deposit_address_partners" ("deposit_address_partner_deposit_address");
-- Create index "depositaddresspartner_deposit_address_partner_partner" to table: "deposit_address_partners"
CREATE INDEX "depositaddresspartner_deposit_address_partner_partner" ON "deposit_address_partners" ("deposit_address_partner_partner");
