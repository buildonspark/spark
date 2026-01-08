# spark-types-entrypoint-repro

Minimal TypeScript repro for the SDK type mismatch between:

- the type returned from `SparkWallet.createLightningInvoice()` (imported from `@buildonspark/spark-sdk`), and
- the `LightningReceiveRequest` type imported from `@buildonspark/spark-sdk/types`.

## Repro

From `sdks/js`:

```bash
yarn build:packages
yarn workspace spark-types-entrypoint-repro typecheck
```
