# Artillery Engine for Spark Testing

Artillery load testing engine for the Spark SDK, providing comprehensive performance and integration testing capabilities with a pool-based wallet management system.

## Installation

```bash
# From the artillery directory
yarn install
yarn build

# Link artillery engine locally (optional)
yarn artillery:install
```

## Requirements

- Node.js 16.0.0 or higher
- Artillery 2.0.0 or higher
- Spark SDK dependencies

## Configuration

### Environment Variables

- `SPARK_NETWORK`: Network to use (MAINNET, TESTNET, SIGNET, REGTEST, LOCAL)
- `DEBUG`: Use comma-separated values (artillery namespaces) to run scenario in debug mode (artillery:runner, runner)

## Available Actions

### Pool Management Actions

#### `initializePools`
Creates wallet pools before test execution.
```yaml
- initializePools:
    pools:
      - name: "main"           # Pool identifier
        amount: 100            # Number of wallets
        amountEnvName: "SIZE"  # Environment variable for pool size
        mnemonicsFile: "./mnemonics/test.txt"  # Deterministic wallets
```

#### `fundPoolsInBatchL1`
Funds multiple wallets in one transaction.
```yaml
- fundPoolsInBatchL1:
    amountPerWallet: 1000000   # Satoshis per wallet
    poolNames: ["main", "hot"] # Specific pools to fund
```

#### `mineBlocks`
Mines blocks for confirmations.
```yaml
- mineBlocks:
    blocks: 6  # Number of blocks to mine
```

#### `cleanupPools`
Cleans up all wallet pools after tests.

### Wallet Actions

#### `lockWallets`
Locks wallets from specific pools for exclusive use during the scenario.
```yaml
before:
  - lockWallets:
      wallets:
        - { name: "sender", pool: "funded" }
        - { name: "receiver", pool: "empty" }
```

#### `getBalance`
Gets balance with optional verification.
```yaml
- getBalance:
    name: "alice"           # Wallet name (optional)
    expectedAmount: 50000   # Expected balance (optional)
    storeAs: "balance"     # Variable name to store result
```

#### `setTransferAmount`
Sets dynamic transfer amounts.
```yaml
# Fixed amount
- setTransferAmount:
    amount: 50000
    
# Random amount
- setTransferAmount:
    min: 10000
    max: 100000
    storeAs: "randomAmount"
```

#### `lockWallets`/`unlockWallets`
Manages wallet locking for scenarios.

### Transfer Actions

#### `transfer`
Executes transfers using amount from context or parameters.
```yaml
- transfer:
    amount: 50000          # Fixed amount
    name: "alice"         # Sender name (optional)
    receiverName: "bob"   # Receiver name (optional)
```

### SDK Wrapper Actions

#### `setupVirtualUser`
Initialize virtual user with pool wallet.

#### `simpleTransfer`
Basic transfer operation.

#### `simpleDeposit`
Generate deposit address.

#### `queryPending`
Query pending transfers.

#### `claimPending`
Claim pending transfers.

#### `switchToReceiverWallet`/`switchToSenderWallet`
Switch wallet context.

### Named Wallet Actions

#### `initNamedWallet`
Create globally accessible named wallet.
```yaml
- initNamedWallet:
    name: "treasury"
    mnemonic: "your twelve word mnemonic phrase here"
```

#### `transferToNamed`
Transfer to named wallet.

#### `queryPendingNamed`
Query pending for named wallet.

#### `claimPendingNamed`
Claim pending for named wallet.

## Available Scenarios

- **transfer-simple.yml** - Basic sat transfers with balance verification
- **token-simple.yml** - Basic token operations (announcement, mint, transfer)


You can run scenario from directory `sdks/js/packages/artillery` with command (specify necessary scenario):

```bash
artillery run ./config/scenarios/transfer-simple.yml
```

## Architecture

### Engine Structure

- **bitcoin-faucet.ts** – Bitcoin faucet engine logic
- **bitcoin-faucet-wrapper.ts** – Wrapper for Bitcoin faucet integration
- **hooks.ts** – Lifecycle hooks for test management
- **index.ts** – Engine entry point and exports
- **spark-sdk-wrapper.ts** – SDK wrapper for virtual user operations
- **token-actions.ts** – Token-related actions and utilities
- **transfer.ts** – Transfer operations with pool awareness
- **wallet-actions.ts** – Core wallet operations and pool management
- **wallet-pool-manager.ts** – Centralized wallet pool management singleton
- **types/** – TypeScript type definitions

### Pool-Based Architecture

- **Pre-allocated Pools**: Wallets are created in named pools before tests
- **Automatic Locking**: Wallets are locked/unlocked per scenario
- **Batch Operations**: Fund hundreds of wallets in one transaction
- **Resource Efficiency**: Reuse wallets across test runs

## License

See LICENSE file in the repository root.