---
"@buildonspark/spark-sdk": minor
---

- Add `getLightningReceiveQuote` and a `quote` option on `createLightningInvoice`, so a receive can accept an SSP fee quote: the manifest is checked against the requested amount and basis, signed with the wallet identity key, and echoed back verbatim.
- Export `ReceiveQuoteAmountBasis`, `manifestGrossSats`, `manifestFeeSats`, `manifestNetSatsFor` and `validateQuotedManifestAmounts`.
- An invoice issued with a quote is issued for the manifest's gross rather than the requested amount. The two differ whenever a markup applies; without a quote nothing changes.
