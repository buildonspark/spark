# Node Scripts

These scripts are meant to be run with `tsx`. First run `yarn` to resolve the
workspace dependencies, then use the package scripts:

```bash
yarn run example
```

Available script names:

- `example`
- `get-or-create-wallet`
- `create-invoice`
- `deposit-bitcoin`
- `get-all-transfers`
- `get-balance`
- `get-spark-address`
- `get-transfers-with-time-filter`
- `pay-invoices`
- `send-transfer`

The generic wrappers match the CLI-style environments:

```bash
yarn run:local get-balance "<mnemonic>"
yarn run:k8s get-balance "<mnemonic>"
yarn run:mainnet get-balance "<mnemonic>"
```

`LOCAL` uses the SDK's existing local routing:

- `MINIKUBE_IP` unset: `https://localhost:8535-8537`
- `MINIKUBE_IP` set: `https://{i}.spark.minikube.local`

`NUM_SPARK_OPERATORS` is also respected if your local setup uses more than the
default three operators.
