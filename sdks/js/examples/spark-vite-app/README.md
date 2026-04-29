# Spark Vite App

This example app now uses a single dev entrypoint:

```bash
yarn start
```

The app surfaces additional targets based on what is available locally:

- `DEV` appears when private config files exist under `sdks/js/private/config`
- `LOCAL` appears when the app is served from localhost and a local ingress
  host can be resolved
- otherwise the app assumes `PROD`

## Local Kubernetes mode

`yarn start` resolves the local host in this order:

1. `SPARK_LOCAL_INGRESS_HOST`
2. `127.0.0.1` when `kubectl config current-context` looks like `kind` / `kdev`
3. `minikube ip`

If that produces a value, the `LOCAL` target becomes available and proxies
browser traffic to the local Kubernetes ingress:

- `https://0.spark-web.minikube.local` via `/spark-rpc/0`
- `https://1.spark-web.minikube.local` via `/spark-rpc/1`
- `https://2.spark-web.minikube.local` via `/spark-rpc/2`
- `http://mempool.minikube.local/api` via `/spark-electrs`
- `http://app.minikube.local` via `/spark-ssp`
- `http://<local ingress host>:8332` via `/bitcoin-rpc`

You can explicitly override the detected host before starting the app with:

- `SPARK_LOCAL_INGRESS_HOST`

## DEV configs

The `DEV` target does not require a special start command. It appears
automatically when these files exist:

- `sdks/js/private/config/dev-regtest-config.json`
- `sdks/js/private/config/dev-mainnet-config.json`

## DEV deployment

The GitHub Actions workflow `Deploy Spark Vite App` builds this app with:

- `SPARK_VITE_APP_BASE=/app/spark-vite-app/`
- `VITE_SPARK_TARGET=DEV`

and uploads the static build output to:

- `s3://lightspark-dev-web/app/spark-vite-app/`

The `github-actions-spark` role used by the workflow must have `s3:PutObject`
on `lightspark-dev-web/app/spark-vite-app/*`. The workflow intentionally
uploads with `aws s3 cp --recursive` instead of `aws s3 sync --delete` so
deploys do not need bucket-level `s3:ListBucket`; old content-hashed Vite assets
can remain safely.

The app is served through the generic `/app/<app>/` SPA route on
`dev.dev.sparkinfra.net`, so deep links under `/app/spark-vite-app/` route back
to this app's `index.html`.

## Notes

- The `Deposit` section includes a `Fund Locally` button when `LOCAL` is
  selected and the app is opened from localhost. It uses the local bitcoind RPC
  to fund a fresh deposit address, mines 3 confirmation blocks, then claims the
  deposit into the wallet.
- The Bitcoin RPC proxy uses `BITCOIN_RPC_USER` / `BITCOIN_RPC_PASSWORD` if
  provided and otherwise defaults to `testutil` / `testutilpassword`. You can
  also override the backend RPC endpoint with `BITCOIN_RPC_URL`. The proxy is
  restricted to localhost callers.
