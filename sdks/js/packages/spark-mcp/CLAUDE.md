# spark-mcp package

MCP server wrapping `@buildonspark/spark-sdk`, exposing Spark wallet operations as Claude Code tools.

## Keep docs up to date

When making changes to this package, update **both** `README.md` and this file (`CLAUDE.md`) to reflect them:

### README.md

- **New tool added** → add a row to the appropriate table in the "Available tools" section
- **Tool removed or renamed** → update or remove the corresponding row
- **New environment variable** → add a row to the "Environment variables" table
- **Build/test commands change** → update the "Development" section
- **Package published to npm** → update the installation section (remove "once published" qualifier from the npx instructions and remove the local build workaround)

### CLAUDE.md (this file)

- **Tool added/removed/renamed** → update the tool comment on the relevant file in the "Package structure" tree
- **File added/removed/renamed** → update the "Package structure" tree
- **SDK type shape discovered to be wrong** → update the "SDK type notes" section

## Package structure

```
src/
├── index.ts          # Entry point: MCP server instructions, stdio transport
├── config.ts         # ServerConfig resolution (BITCOIN_NETWORK → LOCAL | REGTEST | MAINNET)
├── wallet.ts         # resolveWallet() + createFreshWallet() — stateless wallet init
├── utils.ts          # formatSats(), errorMessage(), formatTransferList()
└── tools/
    ├── index.ts      # registerAllTools() — registers all tools with the MCP server
    ├── wallet.ts     # spark_get_balance, spark_get_spark_address
    ├── create-wallet.ts # spark_create_wallet — generates new wallet, returns mnemonic
    ├── deposits.ts   # spark_get_deposit_address, spark_claim_deposit
    ├── deposit-flow.ts # spark_deposit (combined fund+claim, LOCAL only)
    ├── funding.ts    # spark_fund_address (LOCAL only, Bitcoin RPC)
    ├── transfers.ts  # spark_send_transfer, spark_send_multi_transfer, spark_get_transfer, spark_list_transfers
    ├── lightning.ts  # spark_create_invoice, spark_pay_invoice, spark_get_lightning_fee_estimate
    └── withdrawals.ts # spark_get_withdrawal_fee_quote, spark_withdraw
```

## Adding or removing a tool

1. Implement a `handle*` function in the appropriate file under `src/tools/`
2. Register it in `src/tools/index.ts` via `server.tool(...)`
3. Add a test in `src/tests/`
4. Add a row to the tool table in `README.md` (or remove it)
5. Update the package structure tree in this file (`CLAUDE.md`)

## Configuration model

The server uses one environment variable: `BITCOIN_NETWORK` (`LOCAL` | `REGTEST` | `MAINNET`), defaulting to `REGTEST`. This maps directly to the SDK's `NetworkType` — no intermediate translation layer.

| Network   | SDK network | Infrastructure                           |
| --------- | ----------- | ---------------------------------------- |
| `LOCAL`   | `LOCAL`     | Self-hosted regtest (localhost/minikube) |
| `REGTEST` | `REGTEST`   | Lightspark-hosted regtest                |
| `MAINNET` | `MAINNET`   | Production Bitcoin                       |

### Per-call network override

Every tool exposes an optional `network` parameter (`LOCAL` | `REGTEST` | `MAINNET`). The tool registration layer creates a bound resolve function via `makeResolve(network)` that passes the override to `resolveWallet()`. Handler signatures are unchanged — they still accept `ResolveFn = (mnemonic?) => Promise<SparkWallet>`.

This lets a single server instance operate on multiple networks per-call.

### Funding tool gating

`spark_fund_address` and `spark_deposit` are **only registered** when `config.defaultNetwork === "LOCAL"`. They also have runtime guards that reject calls unless the effective network is `LOCAL`. Passing `network: "MAINNET"` or `network: "REGTEST"` to a funding tool returns an error.

## SDK routing details

The SDK determines endpoints based on its network config:

- **`LOCAL`** — for minikube or run-everything.sh. Uses `getLocalSigningOperators()` which reads `MINIKUBE_IP`:
  - `MINIKUBE_IP` set → `https://{i}.spark.minikube.local` SOs + `http://mempool.minikube.local/api` electrs
  - `MINIKUBE_IP` unset → `https://localhost:{8535+i}` SOs + `http://127.0.0.1:30000` electrs
- **`REGTEST`** — Lightspark-hosted shared regtest. Uses external SSP (`api.lightspark.com`) and external electrs (`regtest-mempool.us-west-2.sparkinfra.net`).
- **`MAINNET`** — production. Uses external SSP (`api.lightspark.com`) and `mempool.space` electrs.

`SPARK_ENDPOINT` does not exist — `ConfigOptions` has no such field. Pass `MINIKUBE_IP` via the MCP config `env` block; the SDK reads it from `process.env` automatically.

## SDK type notes

These SDK types have non-obvious shapes — don't guess, verify against the SDK source:

- `getBalance()` → `{ balance: bigint }` (field is `balance`, not `sats`)
- `claimDeposit()` → returns `WalletLeaf[]`; leaf value is `leaf.value: number` (not `valueSats`)
- `createLightningInvoice()` → returns `LightningReceiveRequest`; BOLT11 string is at `.invoice.encodedInvoice`
- `getLightningSendFeeEstimate()` → returns plain `number` (not an object)
- `getWithdrawalFeeQuote()` → params are `{ amountSats, withdrawalAddress }` (not `onchainAddress`)
- `getTransfers()` → returns `{ transfers: WalletTransfer[], offset: number }` (not a plain array)
- `WalletTransfer` fields: `totalValue: number`, `createdTime: Date | undefined`

## Build

```bash
# From repo root:
mise build-js-packages

# From sdks/js/:
yarn build:packages

# Package only:
cd sdks/js/packages/spark-mcp && yarn build
```

## Tests

```bash
cd sdks/js/packages/spark-mcp && yarn test
```

Tests use dependency injection — `handle*` functions accept an optional `resolve` parameter (defaults to `resolveWallet`). Tests pass a mock resolve function that returns a mock wallet object directly, with no SDK calls.
