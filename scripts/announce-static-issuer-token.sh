#!/bin/bash

STATIC_ISSUER_WALLET_ADDRESS="bcrt1q2mgym77n8ta8gn48xtusyrd6wr5uhecajyshku"
echo "Sending 1 BTC to static issuer test wallet: $STATIC_ISSUER_WALLET_ADDRESS"
kubectl -n bitcoin exec regtest-bitcoind-0 -c bitcoind -- \
    bitcoin-cli \
    -conf=/etc/bitcoin/bitcoin.conf \
    -datadir=/data \
    sendtoaddress "$STATIC_ISSUER_WALLET_ADDRESS" 1

kubectl -n bitcoin exec regtest-bitcoind-0 -c bitcoind -- \
    bitcoin-cli \
    -conf=/etc/bitcoin/bitcoin.conf \
    -datadir=/data \
    generatetoaddress 6 "$STATIC_ISSUER_WALLET_ADDRESS"

cd "$(dirname "$0")/../sdks/js/" || exit 1
echo "Building spark-token-cli TypeScript CLI with yarn..."
yarn install --frozen-lockfile
yarn build --filter=@buildonspark/spark-token-cli
cd examples/spark-token-cli || exit 1
yarn run announce-token

kubectl -n bitcoin exec regtest-bitcoind-0 -c bitcoind -- \
    bitcoin-cli \
    -conf=/etc/bitcoin/bitcoin.conf \
    -datadir=/data \
    generatetoaddress 6 "$STATIC_ISSUER_WALLET_ADDRESS"
