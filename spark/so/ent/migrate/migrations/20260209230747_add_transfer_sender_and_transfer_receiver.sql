-- Create "transfer_receivers" table
CREATE TABLE "transfer_receivers" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "identity_pubkey" bytea NOT NULL, "status" character varying NOT NULL, "completion_time" timestamptz NULL, "transfer_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "transfer_receivers_transfers_transfer" FOREIGN KEY ("transfer_id") REFERENCES "transfers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "transferreceiver_identity_pubkey" to table: "transfer_receivers"
CREATE INDEX "transferreceiver_identity_pubkey" ON "transfer_receivers" ("identity_pubkey");
-- Create index "transferreceiver_transfer_id_identity_pubkey" to table: "transfer_receivers"
CREATE UNIQUE INDEX "transferreceiver_transfer_id_identity_pubkey" ON "transfer_receivers" ("transfer_id", "identity_pubkey");
-- Create index "transferreceiver_transfer_id_status" to table: "transfer_receivers"
CREATE INDEX "transferreceiver_transfer_id_status" ON "transfer_receivers" ("transfer_id", "status");
-- Create "transfer_senders" table
CREATE TABLE "transfer_senders" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "identity_pubkey" bytea NOT NULL, "transfer_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "transfer_senders_transfers_transfer" FOREIGN KEY ("transfer_id") REFERENCES "transfers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "transfersender_identity_pubkey" to table: "transfer_senders"
CREATE INDEX "transfersender_identity_pubkey" ON "transfer_senders" ("identity_pubkey");
-- Create index "transfersender_transfer_id_identity_pubkey" to table: "transfer_senders"
CREATE UNIQUE INDEX "transfersender_transfer_id_identity_pubkey" ON "transfer_senders" ("transfer_id", "identity_pubkey");
