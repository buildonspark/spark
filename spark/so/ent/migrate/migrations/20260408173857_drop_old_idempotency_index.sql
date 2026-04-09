-- atlas:txmode none

-- Drop index "idempotency_keys_idempotency_key_method_name" from table: "idempotency_keys"
DROP INDEX CONCURRENTLY "idempotency_keys_idempotency_key_method_name";
