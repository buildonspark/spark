---
"@buildonspark/spark-sdk": patch
---

Scope transfer claims to the wallet's own receiver leaves before verification and claim, so a full multi-receiver query result claims only the caller's leaves. No-op for single-receiver and legacy transfers.
