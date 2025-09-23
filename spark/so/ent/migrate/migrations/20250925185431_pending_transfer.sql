-- Create "pending_send_transfers" table
CREATE TABLE "pending_send_transfers" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "transfer_id" uuid NOT NULL, "status" character varying NOT NULL DEFAULT 'STARTED', PRIMARY KEY ("id"));
-- Create index "pending_send_transfers_transfer_id_key" to table: "pending_send_transfers"
CREATE UNIQUE INDEX "pending_send_transfers_transfer_id_key" ON "pending_send_transfers" ("transfer_id");
-- Create index "pendingsendtransfer_status" to table: "pending_send_transfers"
CREATE INDEX "pendingsendtransfer_status" ON "pending_send_transfers" ("status");
-- Create index "pendingsendtransfer_transfer_id" to table: "pending_send_transfers"
CREATE UNIQUE INDEX "pendingsendtransfer_transfer_id" ON "pending_send_transfers" ("transfer_id");
