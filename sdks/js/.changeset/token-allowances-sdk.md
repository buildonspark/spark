---
"@buildonspark/spark-sdk": minor
---

- Add the token allowance client surface: `createTokenAllowance`, `revokeTokenAllowance`, `queryTokenAllowances`, `startAllowancePull`, and `commitAllowancePull` on `SparkWallet`, plus `TokenAllowanceService`, the allowance statement hashers, and `verifyAllowanceRecord` for verifying queried grants client-side.
