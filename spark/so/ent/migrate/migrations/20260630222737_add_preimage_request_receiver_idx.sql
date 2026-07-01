-- atlas:txmode none

-- Create index "preimagerequest_receiver_identity_pubkey" to table: "preimage_requests"
CREATE INDEX CONCURRENTLY IF NOT EXISTS "preimagerequest_receiver_identity_pubkey" ON "preimage_requests" ("receiver_identity_pubkey");
