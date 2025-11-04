# Spark Bare App

Simple scripts demonstrating [bare](https://bare.pears.com/) support for @buildonspark/spark-sdk.

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
