---
"@buildonspark/spark-sdk": minor
---

- `getLightningReceiveQuote` accepts a `partnerJwt`, sent as the `x-partner-jwt` header on the quote request only. Without one the quote comes back feeless and `attributionStatus` reports why.
- **Breaking:** `LightningReceiveQuote.attributionStatus` is now optional, so a quote rebuilt from its wire values (serialized manifest plus issuer signature) is representable. Consumers reading the field must narrow or default it.
