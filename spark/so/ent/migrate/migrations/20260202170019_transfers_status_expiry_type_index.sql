-- Create index "transfer_status_expiry_time_type" to table: "transfers"
CREATE INDEX "transfer_status_expiry_time_type" ON "transfers" ("status", "expiry_time", "type");
