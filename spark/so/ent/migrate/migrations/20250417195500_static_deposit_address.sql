-- Modify "deposit_addresses" table
ALTER TABLE "deposit_addresses" ADD COLUMN "is_static" boolean NOT NULL DEFAULT false;
