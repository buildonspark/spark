-- atlas:txmode none

-- Re-create old 2-column unique index for backwards-compatible rolling deploys.
-- Old SO instances use ON CONFLICT(idempotency_key, method_name) which requires this index.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "idempotency_keys_idempotency_key_method_name" ON "idempotency_keys" ("idempotency_key", "method_name");
-- Create new 3-column unique index if not exists (may already exist from the previous migration).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "idempotency_keys_key_method_identity" ON "idempotency_keys" ("idempotency_key", "method_name", "identity_public_key");
