# Spark Bare App

Simple scripts demonstrating [bare](https://bare.pears.com/) support for @buildonspark/spark-sdk.

The direct script commands are still available:

```bash
yarn get-or-create-wallet
yarn transfer <spark-address> <sats>
```

The generic wrappers match the CLI-style environments:

```bash
yarn run get-or-create-wallet
yarn run:local get-or-create-wallet
yarn run:k8s get-or-create-wallet
yarn run:mainnet get-or-create-wallet
```

`LOCAL` uses the SDK's existing local routing:

- `MINIKUBE_IP` unset: `https://localhost:8535-8537`
- `MINIKUBE_IP` set: `https://{i}.spark-web.minikube.local`

`NUM_SPARK_OPERATORS` is also respected if your local setup uses more than the
default three operators. Scripts that need SSP services, such as Lightning or
static deposit flows, still expect a local SSP to be running.

Besides these there's an even better way of exploring spark-sdk support in bare is to run it with --inspect:

```
yarn bare --inspect
```

Then navigate to chrome://inspect and you should see the bare target there to attach to. From there you can run things like:

```
const { SparkWallet, BareSparkSigner } = require("@buildonspark/bare");

const { wallet: w1 } = await SparkWallet.initialize({ signer: new BareSparkSigner() })
await w1.getBalance()
```
