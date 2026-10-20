---
"@buildonspark/spark-sdk": patch
---

- Depend on `ua-parser-js` 1.x (MIT) instead of 2.x (AGPL-3.0-or-later), so apps that bundle the SDK no longer ship AGPL code. The client environment label is unchanged: 1.x returns the same browser and OS name and version fields that `parseUserAgent` reads.
