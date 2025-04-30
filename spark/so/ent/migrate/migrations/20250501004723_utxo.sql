-- Create "utxos" table
CREATE TABLE "utxos" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "block_height" bigint NOT NULL, "txid" bytea NOT NULL, "vout" bigint NOT NULL, "amount" bigint NOT NULL, "network" character varying NOT NULL, "deposit_address_utxo" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "utxos_deposit_addresses_utxo" FOREIGN KEY ("deposit_address_utxo") REFERENCES "deposit_addresses" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "utxo_network_txid_vout" to table: "utxos"
CREATE UNIQUE INDEX "utxo_network_txid_vout" ON "utxos" ("network", "txid", "vout");
