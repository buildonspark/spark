# @buildonspark/spark-mcp

An MCP (Model Context Protocol) server that exposes Spark Bitcoin/Lightning wallet operations as tools for Claude Code agents. Run it locally — no server to host, no custody risk.

## How it works

The server runs as a local stdio process on your machine. `SPARK_MNEMONIC` is optional — if set, tools use it as the default wallet. You can also call `spark_create_wallet` to generate a new wallet on demand, or pass a `mnemonic` parameter to any tool to operate on a specific wallet. Your keys never leave your machine.

## Installation

MCP servers are configured in a JSON file that Claude Code reads at startup. There are two scopes:

- **Global** (`~/.claude.json`) — available in all your Claude Code sessions
- **Project** (`.mcp.json` in the repo root) — checked into the repo, available to everyone who clones it

Add the server entry to whichever file fits your use case. If the file doesn't exist yet, create it.

### Via npx (once published)

No installation needed — Claude Code spawns it automatically:

```json
{
  "mcpServers": {
    "spark": {
      "command": "npx",
      "args": ["-y", "@buildonspark/spark-mcp"],
      "env": {
        "SPARK_MNEMONIC": "your twelve word mnemonic phrase here",
        "BITCOIN_NETWORK": "MAINNET"
      }
    }
  }
}
```

### Via local build

Build the package first:

```bash
cd sdks/js && yarn build:packages
# or: mise build-js-packages
```

Then add to `~/.claude.json` (or `.mcp.json` in the project root). See [Configuration examples](#configuration-examples) below.

## Configuration

### Environment variables

| Variable               | Required | Description                                                                                                                                                                                |
| ---------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `BITCOIN_NETWORK`      | No       | Bitcoin network: `LOCAL`, `REGTEST` (default), or `MAINNET`                                                                                                                                |
| `SPARK_MNEMONIC`       | No       | Default BIP39 mnemonic (12 or 24 words). Omit to use `spark_create_wallet` or pass `mnemonic` per-tool call.                                                                               |
| `MINIKUBE_IP`          | No       | Set to your minikube IP (e.g. `192.168.49.2`) to route to `https://{i}.spark.minikube.local`. Omit when using `run-everything.sh` — the SDK routes to `localhost:8535-8539` automatically. |
| `BITCOIN_RPC_URL`      | No       | Bitcoin JSON-RPC URL for `spark_fund_address`. Defaults to `http://{MINIKUBE_IP}:8332` or `http://127.0.0.1:8332`.                                                                         |
| `BITCOIN_RPC_USER`     | No       | Bitcoin RPC username (default: `testutil`)                                                                                                                                                 |
| `BITCOIN_RPC_PASSWORD` | No       | Bitcoin RPC password (default: `testutilpassword`)                                                                                                                                         |

### Networks

The server uses a single `BITCOIN_NETWORK` value that maps directly to the SDK's network type:

| Network   | Infrastructure                                      | Signing operators   | Funding                                             |
| --------- | --------------------------------------------------- | ------------------- | --------------------------------------------------- |
| `LOCAL`   | Self-hosted regtest (minikube or run-everything.sh) | Local (localhost)   | Bitcoin RPC (`spark_fund_address`, `spark_deposit`) |
| `REGTEST` | Lightspark-hosted regtest                           | Lightspark-operated | Faucet or external wallet                           |
| `MAINNET` | Production Bitcoin                                  | Lightspark-operated | External wallet                                     |

`REGTEST` is the default when `BITCOIN_NETWORK` is not set.

Every tool accepts an optional `network` parameter (`LOCAL`, `REGTEST`, or `MAINNET`) to override the server's default for that call. This lets a single server instance operate on multiple networks.

### Configuration examples

**Lightspark-hosted regtest (default — no config needed):**

```json
{
  "mcpServers": {
    "spark": {
      "command": "node",
      "args": ["/path/to/spark/sdks/js/packages/spark-mcp/dist/index.js"],
      "env": {
        "SPARK_MNEMONIC": "your twelve word mnemonic phrase here"
      }
    }
  }
}
```

**Production (mainnet):**

```json
{
  "mcpServers": {
    "spark": {
      "command": "node",
      "args": ["/path/to/spark/sdks/js/packages/spark-mcp/dist/index.js"],
      "env": {
        "BITCOIN_NETWORK": "MAINNET",
        "SPARK_MNEMONIC": "your twelve word mnemonic phrase here"
      }
    }
  }
}
```

**Local development (minikube):**

```json
{
  "mcpServers": {
    "spark-local": {
      "command": "node",
      "args": ["/path/to/spark/sdks/js/packages/spark-mcp/dist/index.js"],
      "env": {
        "BITCOIN_NETWORK": "LOCAL",
        "MINIKUBE_IP": "192.168.49.2"
      }
    }
  }
}
```

**Local development (run-everything.sh):**

```json
{
  "mcpServers": {
    "spark-local": {
      "command": "node",
      "args": ["/path/to/spark/sdks/js/packages/spark-mcp/dist/index.js"],
      "env": {
        "BITCOIN_NETWORK": "LOCAL"
      }
    }
  }
}
```

### Working with multiple wallets

Call `spark_create_wallet` to generate a new wallet on the fly. Save the returned mnemonic, then pass it as the `mnemonic` parameter to any subsequent tool call to operate on that wallet.

For a persistent default wallet, set `SPARK_MNEMONIC` in the server's `env` block — tools will use it when no `mnemonic` is passed.

### Switching networks per-call

Every tool accepts an optional `network` parameter (`LOCAL`, `REGTEST`, or `MAINNET`) to override the server default for that call:

```
spark_get_balance()                   → uses server default (e.g., REGTEST)
spark_get_balance(network: "MAINNET") → uses MAINNET for this call only
spark_get_balance(network: "LOCAL")   → uses LOCAL for this call only
```

Funding tools (`spark_fund_address`, `spark_deposit`) only work on the `LOCAL` network.

## Available tools

### Wallet

| Tool                      | Description                                                                                           |
| ------------------------- | ----------------------------------------------------------------------------------------------------- |
| `spark_create_wallet`     | Generate a new wallet. Returns mnemonic + Spark address                                               |
| `spark_get_balance`       | Get current balance in satoshis                                                                       |
| `spark_get_spark_address` | Get the wallet's Spark address for receiving transfers                                                |
| `spark_disconnect_wallet` | Disconnect a cached wallet, stopping background streams. Prevents auto-claim until the next tool call |

### Deposits (Bitcoin L1 → Spark)

| Tool                        | Description                                                                   |
| --------------------------- | ----------------------------------------------------------------------------- |
| `spark_get_deposit_address` | Get a Bitcoin address to fund the wallet                                      |
| `spark_claim_deposit`       | Claim a confirmed on-chain deposit by transaction ID                          |
| `spark_deposit`             | One-step deposit: get address, fund, claim, and wait for balance (LOCAL only) |
| `spark_fund_address`        | Fund a Bitcoin address from the local regtest node (LOCAL only)               |

`spark_deposit` and `spark_fund_address` are only registered when `BITCOIN_NETWORK` is `LOCAL`. They require a locally accessible bitcoind and do not appear on REGTEST or MAINNET.

### Transfers (off-chain, Spark → Spark)

| Tool                        | Description                                                       |
| --------------------------- | ----------------------------------------------------------------- |
| `spark_send_transfer`       | Send sats to a Spark address (instant, off-chain)                 |
| `spark_send_multi_transfer` | Send sats to multiple Spark addresses in a single atomic transfer |
| `spark_get_transfer`        | Get the status of a transfer by ID                                |
| `spark_list_transfers`      | List the 10 most recent transfers                                 |

### Lightning

| Tool                               | Description                                              |
| ---------------------------------- | -------------------------------------------------------- |
| `spark_create_invoice`             | Create a BOLT11 invoice to receive a Lightning payment   |
| `spark_pay_invoice`                | Pay a BOLT11 invoice                                     |
| `spark_get_lightning_fee_estimate` | Estimate the fee for paying an invoice before committing |

### Withdrawals (Spark → Bitcoin L1)

| Tool                             | Description                                                 |
| -------------------------------- | ----------------------------------------------------------- |
| `spark_get_withdrawal_fee_quote` | Get a fee quote for withdrawing to a Bitcoin address        |
| `spark_withdraw`                 | Withdraw funds to a Bitcoin L1 address via cooperative exit |

## Funding a wallet (LOCAL)

On LOCAL networks (minikube or run-everything.sh), agents can fund a wallet end-to-end without any manual steps:

```
0. spark_create_wallet                → get a new mnemonic + spark address (save the mnemonic)
1. spark_get_deposit_address(mnemonic) → get a Bitcoin deposit address
2. spark_fund_address(address, 50000) → send regtest BTC and mine 1 block
3. spark_claim_deposit(txid, mnemonic) → claim the confirmed deposit
4. spark_get_balance(mnemonic)         → verify the balance increased
```

`spark_fund_address` calls the local bitcoind via JSON-RPC (`sendtoaddress` + `generatetoaddress`). It reads the RPC endpoint from `BITCOIN_RPC_URL`, defaulting to `http://{MINIKUBE_IP}:8332` or `http://127.0.0.1:8332` for `run-everything.sh`.

This tool is not available on REGTEST or MAINNET networks — on those, fund the deposit address through a faucet or external wallet.

## Usage examples

Once configured, use natural language in Claude Code:

- _"Create a new Spark wallet"_
- _"Create two wallets and send 1000 sats from one to the other"_
- _"What's my Spark balance?"_
- _"Get me a deposit address"_
- _"Send 1000 sats to spark1abc..."_
- _"Create a Lightning invoice for 5000 sats with memo 'coffee'"_
- _"Pay this invoice: lnbc..."_
- _"How much would it cost to pay this invoice before I commit?"_
- _"Withdraw 50000 sats to bc1q..."_
- _"Check my balance on mainnet"_ (uses network override)
- _"Check my local balance"_ (uses network override)

## Development

```bash
# Build
cd sdks/js && yarn build:packages

# Test
cd sdks/js/packages/spark-mcp && yarn test

# Type-check
cd sdks/js/packages/spark-mcp && yarn types
```
