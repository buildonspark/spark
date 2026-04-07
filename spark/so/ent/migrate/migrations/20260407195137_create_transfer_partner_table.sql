-- Create "transfer_partners" table
CREATE TABLE "transfer_partners" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "type" character varying NOT NULL,
  "transfer_partner_partner" uuid NOT NULL,
  "transfer_partner_transfer" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "transfer_partners_partners_partner" FOREIGN KEY ("transfer_partner_partner") REFERENCES "partners" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT "transfer_partners_transfers_transfer" FOREIGN KEY ("transfer_partner_transfer") REFERENCES "transfers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "transferpartner_transfer_partner_partner" to table: "transfer_partners"
CREATE INDEX "transferpartner_transfer_partner_partner" ON "transfer_partners" ("transfer_partner_partner");
-- Create index "transferpartner_transfer_partner_transfer" to table: "transfer_partners"
CREATE UNIQUE INDEX "transferpartner_transfer_partner_transfer" ON "transfer_partners" ("transfer_partner_transfer");
