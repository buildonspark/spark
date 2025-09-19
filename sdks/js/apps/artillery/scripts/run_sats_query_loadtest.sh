#!/usr/bin/env bash


# Weight means the percentage of calls be emitted during the test
QUERY_NODES_WEIGHT=45
QUERY_PENDING_TRANSFERS_WEIGHT=15
SUBSCRIBE_TO_EVENTS_WEIGHT=10
GENERATE_DEPOSIT_ADDRESS_WEIGHT=20
QUERY_ALL_TRANSFERS_WEIGHT=10
CONFIG_PATH="../../examples/spark-cli/config/loadtest-regtest-config.json"
OUTFILE="./config/scenarios/sats_query_loadtest_test.yml"

# Parse flags
while [[ $# -gt 0 ]]; do
  case "$1" in
    -qn|--queryNodes)
      QUERY_NODES_WEIGHT="$2"
      shift 2
      ;;
    -qpt|--queryPendingTransfers)
      QUERY_PENDING_TRANSFERS_WEIGHT="$2"
      shift 2
      ;;
    -ste|--subscribeToEvents)
      SUBSCRIBE_TO_EVENTS_WEIGHT="$2"
      shift 2
      ;;
    -gda|--generateDepositAddress)
      GENERATE_DEPOSIT_ADDRESS_WEIGHT="$2"
      shift 2
      ;;
    -qat|--queryAllTransfers)
          QUERY_ALL_TRANSFERS_WEIGHT="$2"
          shift 2
          ;;
    -c|--config)
          CONFIG_PATH="$2"
          shift 2
          ;;
    *)
       "Unknown option: $1"
      exit 1
      ;;
  esac
done


# Generate the templated sats_loadtest_scenario
cat > "$OUTFILE" <<EOF
config:
  target: "http://localhost"
  engines:
    spark: {}
  phases:
    - duration: 10
      arrivalRate: 10

beforeTest:
  - initializePools:
      pools:
        - name: "issuer-pool"
          mnemonicsFile: "./mnemonics/batch-source-wallets.txt"
          amount: 200
          minBalance: 15

        - name: "recipient-pool"
          mnemonicsFile: "./mnemonics/batch-recipient-wallets.txt"
          amount: 200

scenarios:
  - name: "Query Nodes"
    weight: $QUERY_NODES_WEIGHT
    engine: spark
    before:
      - lockWallets:
          wallets:
            - { walletName: "issuer", pool: "issuer-pool" }
    flow:
      - queryNodes:
          walletName: "issuer"
    after:
      unlockWallets:
        wallets: ["issuer"]

  - name: "Query pending transfers"
    weight: $QUERY_PENDING_TRANSFERS_WEIGHT
    engine: spark
    before:
      - lockWallets:
          wallets:
            - { walletName: "issuer", pool: "issuer-pool" }
    flow:
      - queryPendingTransfers:
          walletName: "issuer"
    after:
      unlockWallets:
        wallets: ["issuer"]

  - name: "Subscribe to events"
    weight: $SUBSCRIBE_TO_EVENTS_WEIGHT
    engine: spark
    before:
      - lockWallets:
          wallets:
            - { walletName: "issuer", pool: "issuer-pool" }
    flow:
      - subscribeToEvents:
          walletName: "issuer"

    after:
      unlockWallets:
        wallets: ["issuer"]

  - name: "Generate deposit address"
    weight: $GENERATE_DEPOSIT_ADDRESS_WEIGHT
    engine: spark
    before:
      - lockWallets:
          wallets:
            - { walletName: "issuer", pool: "issuer-pool" }
    flow:
      - generateDepositAddress:
          walletName: "issuer"
    after:
      unlockWallets:
        wallets: [ "issuer" ]

  - name: "Query all transfers"
    weight: $QUERY_ALL_TRANSFERS_WEIGHT
    engine: spark
    before:
      - lockWallets:
          wallets:
            - { walletName: "issuer", pool: "issuer-pool" }
    flow:
      - queryAllTransfers:
          walletName: "issuer"
    after:
      unlockWallets:
        wallets: ["issuer"]
EOF

echo "✅ YAML written to $OUTFILE"

# Set the config file environment variable
export CONFIG_FILE=$CONFIG_PATH

# Running the generated scenario
echo "Running the sats query test scenario..."
yarn run artillery run $OUTFILE && rm -rf .artillery-locks-db
echo "Running scenario finished!"
