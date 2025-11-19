-- Modify "preimage_requests" table
ALTER TABLE "preimage_requests" ADD COLUMN "sender_identity_pubkey" bytea NULL;
-- Create index "preimagerequest_sender_identity_pubkey" to table: "preimage_requests"
CREATE INDEX "preimagerequest_sender_identity_pubkey" ON "preimage_requests" ("sender_identity_pubkey");
