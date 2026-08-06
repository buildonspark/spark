---
"@buildonspark/spark-sdk": minor
---

**Breaking:** `payLightningInvoice` no longer accepts `idempotencyKey`. Pass `transferId` (a UUID) instead, or omit it and one is generated per call.

Fixed an issue where `idempotencyKey` wasn't idempotent for Lightning sends: it was ignored entirely when the payment fell back to a Spark transfer, and it was never forwarded to the SSP, so it only ever deduplicated the preimage swap RPC. `transferId` replaces it as the single dedup identity across every rail a payment touches — Spark fallback transfer, preimage swap, and SSP admission — so retrying with the same ID cannot produce a second payment. `IdempotencyOptions` itself is unchanged and still applies to the generic call-option paths.
