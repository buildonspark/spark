-- Modify "deposit_addresses" table
ALTER TABLE "deposit_addresses" ADD COLUMN "availability_confirmed_at" timestamptz NULL;
-- Create index "depositaddress_confirmation_height_is_static" to table: "deposit_addresses"
CREATE INDEX "depositaddress_confirmation_height_is_static" ON "deposit_addresses" ("confirmation_height", "is_static") WHERE (availability_confirmed_at IS NULL);
