---
"@buildonspark/spark-sdk": patch
"@buildonspark/bare": patch
"@buildonspark/cli": patch
---

Prevent background Spark wallet streams, retry timers, and periodic wallet maintenance intervals from keeping Node.js and Bare processes alive after foreground work completes, and clean up Spark CLI wallet connections on exit.
