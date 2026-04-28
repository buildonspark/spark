-- atlas:nolint DS103

-- Bound the ACCESS EXCLUSIVE wait so the instant-deposit write path isn't
-- stalled behind a long-running transaction. Atlas default tx-mode=file
-- wraps this whole file in one transaction, so SET LOCAL applies.
SET LOCAL lock_timeout = '500ms';

-- Modify "utxo_swaps" table
ALTER TABLE "utxo_swaps" DROP COLUMN IF EXISTS "expiry_time";
