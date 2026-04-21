# Spark CLI

An interactive CLI for interacting with the Spark SDK.

## Quick Start

```bash
npx @buildonspark/cli
```

Or install globally:

```bash
npm install -g @buildonspark/cli
spark-cli
```

## Options

```
spark-cli [options]

  --network <network>  Network to connect to (mainnet, regtest, local) [default: regtest]
  --config <path>      Path to a JSON config file
  -v, --version        Print version
  -h, --help           Show this help message
```

### Examples

```bash
spark-cli --network mainnet
spark-cli --network regtest
spark-cli --config ./my-config.json
```

## Example Flow

Here is an example flow that initializes a Spark wallet, deposits funds from the L1 faucet, and transfers sats to a different wallet:

1. `initwallet`
2. `getdepositaddress`
3. Open the [regtest faucet](https://app.lightspark.com/regtest-faucet) and paste in the deposit address from step 2. Press 'Send Funds' to get a transaction hash.
4. Back in the CLI, enter `claimdeposit <txid>` where `<txid>` is the transaction hash from step 3. Your wallet is now funded! (this may take a few seconds; if it doesn't show up, reinitializing your wallet with the mnemonic from step 1 will re-run the claim)
5. Run `getbalance` to see your balance, or `getleaves` to see details about your leaves.
6. Open another terminal and start the CLI again on the same network.
7. Init another wallet and get its spark address with `getsparkaddress`.
8. Back in the first wallet, send a transfer with `sendtransfer <amount> <sparkAddress>`.
9. In the second wallet, run `getbalance` or `getleaves` to confirm the transfer was received.

## Local Development

If developing from the monorepo, you can run the CLI directly with tsx:

```bash
yarn cli            # regtest (default)
yarn cli:mainnet    # mainnet
yarn cli:local      # local network
```

`cli:local` auto-detects the local ingress host in this order:

- `SPARK_LOCAL_INGRESS_HOST`
- `127.0.0.1` when `kubectl config current-context` looks like `kind` / `kdev`
- `minikube ip`
- otherwise no ingress override, which keeps the existing direct `localhost:8535+` flow
