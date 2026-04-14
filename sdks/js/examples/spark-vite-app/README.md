# Spark Vite App

This example app can target local signing operators through the Vite dev server
proxy while keeping the browser on same-origin URLs.

## Run

```bash
yarn start
yarn start:local
yarn start:k8s
yarn start:mainnet
```

## Local SO mode

`start:local` proxies browser traffic to:

- `https://localhost:8535` via `/spark-rpc/0`
- `https://localhost:8536` via `/spark-rpc/1`
- `https://localhost:8537` via `/spark-rpc/2`
- `http://127.0.0.1:30000` via `/spark-electrs`
- `http://127.0.0.1:5000` via `/spark-ssp`

`start:k8s` switches those proxy targets to the local Kubernetes ingress:

- `https://0.spark-web.minikube.local` via `/spark-rpc/0`
- `https://1.spark-web.minikube.local` via `/spark-rpc/1`
- `https://2.spark-web.minikube.local` via `/spark-rpc/2`
- `http://mempool.minikube.local/api` via `/spark-electrs`
- `http://app.minikube.local` via `/spark-ssp`

You can still override any target in `.env.local`:

```bash
VITE_LOCAL_SPARK_OPERATOR_0_TARGET=https://localhost:8535
VITE_LOCAL_SPARK_OPERATOR_1_TARGET=https://localhost:8536
VITE_LOCAL_SPARK_OPERATOR_2_TARGET=https://localhost:8537
VITE_LOCAL_ELECTRS_TARGET=http://127.0.0.1:30000
VITE_LOCAL_SSP_TARGET=http://127.0.0.1:5000
VITE_NUM_SPARK_OPERATORS=3
```

The app's `LOCAL` button uses those same-origin proxy URLs so the browser does
not need to trust the operator certs directly.

## Notes

- The local proxy only exists while running `yarn start*`.
- The Spark repo's `docker compose up --build` path brings up local signing
  operators and electrs. Lightning or other SSP-backed flows still need a local
  SSP if you want those parts of the example to work in `LOCAL`.
