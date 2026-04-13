-- Create "preimage_share_partners" table
CREATE TABLE "preimage_share_partners" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "preimage_share_partner_partner" uuid NOT NULL,
  "preimage_share_partner_preimage_share" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "preimage_share_partners_partners_partner" FOREIGN KEY ("preimage_share_partner_partner") REFERENCES "partners" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT "preimage_share_partners_preimage_shares_preimage_share" FOREIGN KEY ("preimage_share_partner_preimage_share") REFERENCES "preimage_shares" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "preimagesharepartner_preimage_share_partner_partner" to table: "preimage_share_partners"
CREATE INDEX "preimagesharepartner_preimage_share_partner_partner" ON "preimage_share_partners" ("preimage_share_partner_partner");
-- Create index "preimagesharepartner_preimage_share_partner_preimage_share" to table: "preimage_share_partners"
CREATE UNIQUE INDEX "preimagesharepartner_preimage_share_partner_preimage_share" ON "preimage_share_partners" ("preimage_share_partner_preimage_share");
