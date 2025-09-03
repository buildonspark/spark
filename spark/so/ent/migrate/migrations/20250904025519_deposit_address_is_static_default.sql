-- alter table add colum acquires ACCESS EXCLUSIVE lock so no records can be added while this db tx is ongoing
ALTER TABLE "deposit_addresses" ADD COLUMN "is_default" boolean NOT NULL DEFAULT true;

UPDATE deposit_addresses SET is_default = FALSE where is_static = TRUE;

UPDATE deposit_addresses da
SET is_default = TRUE
WHERE da.id IN (
    SELECT DISTINCT ON (owner_identity_pubkey) id
    FROM deposit_addresses
    WHERE is_static = TRUE
    ORDER BY owner_identity_pubkey, create_time DESC
);

CREATE UNIQUE INDEX "depositaddress_network_owner_identity_pubkey" ON "deposit_addresses" ("network", "owner_identity_pubkey") WHERE ((is_static = true) AND (is_default = true));
