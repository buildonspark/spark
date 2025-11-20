Build/watch local spark-sdk:

```sh
cd sdks/js
yarn && yarn build:packages:watch
```

Build docker from spark root:

```sh
docker run --rm --name spark-wallet-node \
  -e SPARK_WALLET_MNEMONIC="MNEMONIC_HERE" \
  spark-wallet-node-repro
```

run:

```sh
docker run --rm --name spark-wallet-node spark-wallet-node-repro
```

disconnect network:

```sh
docker network disconnect bridge spark-wallet-node
```

reconnect network:

```sh
docker network connect bridge spark-wallet-node
```

pause container:

```sh
docker pause spark-wallet-node
```

unpause container:

```sh
docker unpause spark-wallet-node
```
