# Spark Interactive CLI (Ink)

This example provides a basic interactive CLI built with Ink that exercises
`SparkWallet` methods from `@buildonspark/spark-sdk`.

## Setup

From the JS workspace root:

```
cd sdks/js
yarn install
```

Build and run the CLI:

```
cd sdks/js/examples/interactive-cli
yarn build
yarn start
```

## Environment

- `NETWORK` (MAINNET | REGTEST | LOCAL). Defaults to REGTEST.
- `CONFIG_FILE` Optional JSON config file used to override defaults.
- `SPARK_MNEMONIC` Optional mnemonic or seed used when initializing the wallet.

## Notes

- `babel.config.json` uses `@babel/preset-react` and `@babel/preset-typescript`
  to compile `cli.tsx` to `dist/cli.js`.
- This is an example CLI for development/testing only.
