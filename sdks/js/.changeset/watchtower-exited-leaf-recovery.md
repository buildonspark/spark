---
"@buildonspark/spark-sdk": minor
---

Added `getWatchtowerExitedLeaves`, `recoverWatchtowerExitedLeaf`, and `recoverAndBroadcastWatchtowerExitedLeaf`, plus the exported `WatchtowerExitedLeaf` type.

A leaf stops being spendable on Spark once an ancestor's transaction confirms on L1, and until now nothing surfaced it: it left the balance and `getLeaves` with no way to learn it needed attention. `getWatchtowerExitedLeaves` lists those leaves and names the on-chain output each one can still spend; `recoverWatchtowerExitedLeaf` builds and signs a transaction sweeping that output to an address you choose, and the `recoverAndBroadcast` variant publishes it.
