#!/usr/bin/env bash

ARRIVAL_RATE=20
DURATION=10
TRANSFER_LOOP_COUNT=20
MINIMAL_BALANCE=3500
WALLET_EXIT_AMOUNT=2500
CONFIG_PATH="../../examples/spark-cli/config/loadtest-regtest-config.json"
OUTFILE="./config/scenarios/sats_flow_test.yml"

# Parse flags
while [[ $# -gt 0 ]]; do
  case "$1" in
    -ar|--arrivalRate)
      ARRIVAL_RATE="$2"
      shift 2
      ;;
    -d|--duration)
      DURATION="$2"
      shift 2
      ;;
    -lc|--transferLoopCount)
      TRANSFER_LOOP_COUNT="$2"
      shift 2
      ;;
    -c|--config)
      CONFIG_PATH="$2"
      shift 2
      ;;
    -mb|--minimalBalance)
      MINIMAL_BALANCE="$2"
      shift 2
      ;;
    -we|--walletExitAmount)
      WALLET_EXIT_AMOUNT="$2"
      shift 2
      ;;
    *)
       "Unknown option: $1"
      exit 1
      ;;
  esac
done

# Calculate the total amount of vusers be created during the test
VUSERS_COUNT=$(($DURATION * $ARRIVAL_RATE))

# Generate the templated sats_loadtest_scenario
cat > "$OUTFILE" <<EOF
config:
  target: "http://localhost"
  engines:
    spark: {}
  phases:
    - duration: $DURATION
      arrivalRate: $ARRIVAL_RATE

beforeTest:
  - initializePools:
      pools:
        - name: "sender-pool"
          mnemonicsFile: "./mnemonics/batch-source-wallets.txt"
          amount: $VUSERS_COUNT
          minBalance: $MINIMAL_BALANCE
          randomSelection: true

        - name: "receiver-pool"
          mnemonicsFile: "./mnemonics/batch-recipient-wallets.txt"
          amount: $VUSERS_COUNT

scenarios:
  - name: "Sats flow test"
    weight: 1
    engine: spark
    before:
      - lockWallets:
          wallets:
            - { walletName: "sender", pool: "sender-pool" }
            - { walletName: "receiver", pool: "receiver-pool" }
    flow:
      - loop:
          count: $TRANSFER_LOOP_COUNT
          actions:
            - transfer:
                walletName: "sender"
                receiverName: "receiver"
                amount: 10

            - claimTransfer:
                walletName: "receiver"

            - transfer:
                walletName: "receiver"
                receiverName: "sender"
                amount: 10

            - claimTransfer:
                walletName: "sender"

      - getStaticAddress:
          walletName: "receiver"
          storeAs: "receiverInitial"

      - withdraw:
          senderWallet: "sender"
          receiverWallet: "receiverInitial"
          storeAs: "receiverWallet"
          amount: $WALLET_EXIT_AMOUNT

      - claimStaticDeposit:
          walletName: "receiverWallet"
    after:
      unlockWallets:
        wallets: ["sender", "receiver"]
EOF

echo "✅ YAML written to $OUTFILE"

# Set the config file environment variable
export CONFIG_FILE=$CONFIG_PATH

# Running the generated scenario
echo "Running the Token sats test scenario..."
yarn run artillery run $OUTFILE && rm -rf .artillery-locks-db
echo "Running scenario finished!"
