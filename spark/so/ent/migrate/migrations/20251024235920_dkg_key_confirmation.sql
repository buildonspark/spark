-- Drop index "idx_signing_keyshares_coordinator_available" from table: "signing_keyshares"
DROP INDEX "idx_signing_keyshares_coordinator_available";
-- Create index "idx_signing_keyshares_coordinator_available_or_pending" to table: "signing_keyshares"
CREATE INDEX "idx_signing_keyshares_coordinator_available_or_pending" ON "signing_keyshares" ("coordinator_index", "status") WHERE ((status)::text = ANY (ARRAY['AVAILABLE'::text, 'PENDING'::text]));
