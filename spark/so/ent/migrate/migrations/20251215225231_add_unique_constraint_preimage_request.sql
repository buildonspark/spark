-- Drop index "preimagerequest_payment_hash_receiver_identity_pubkey" from table: "preimage_requests"
DROP INDEX "preimagerequest_payment_hash_receiver_identity_pubkey";
-- Create index "preimagerequest_payment_hash_receiver_identity_pubkey" to table: "preimage_requests"
CREATE UNIQUE INDEX "preimagerequest_payment_hash_receiver_identity_pubkey" ON "preimage_requests" ("payment_hash", "receiver_identity_pubkey") WHERE ((status)::text <> 'RETURNED'::text);
