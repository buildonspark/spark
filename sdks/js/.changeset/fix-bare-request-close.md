---
"@buildonspark/spark-sdk": patch
"@buildonspark/bare": patch
---

- Reject Bare RPCs immediately when the underlying request closes before response headers, avoiding a 15-second transport delay that could keep backgrounded Bare runtimes alive.
