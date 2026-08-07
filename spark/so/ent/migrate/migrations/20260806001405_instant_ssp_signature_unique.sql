-- atlas:txmode none

-- A CONCURRENTLY build that fails mid-flight leaves an INVALID index behind, which
-- enforces nothing and which IF NOT EXISTS would then silently skip on retry.
DROP INDEX CONCURRENTLY IF EXISTS "utxoswap_ssp_signature";

-- Create index "utxoswap_ssp_signature" to table: "utxo_swaps"
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "utxoswap_ssp_signature" ON "utxo_swaps" ("ssp_signature") WHERE (((request_type)::text = 'INSTANT'::text) AND ((status)::text <> 'CANCELLED'::text));
