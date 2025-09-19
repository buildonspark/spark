#!/usr/bin/env bash


ARRIVAL_RATE=50
DURATION=10
TRANSFER_LOOP_COUNT=10
CONFIG_PATH="../../examples/spark-cli/config/loadtest-regtest-config.json"
OUTFILE="./config/scenarios/token_flow_test.yml"

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
        - name: "issuer-pool"
          mnemonicsFile: "./mnemonics/batch-source-wallets.txt"
          amount: $VUSERS_COUNT

        - name: "recipient-pool"
          mnemonicsFile: "./mnemonics/batch-recipient-wallets.txt"
          amount: $VUSERS_COUNT

scenarios:
  - name: "Token flow test"
    weight: 1
    engine: spark
    before:
      - lockWallets:
          wallets:
            - { walletName: "issuer", pool: "issuer-pool" }
            - { walletName: "recipient", pool: "recipient-pool" }
    flow:
      - announceToken:
          walletName: "issuer"
      - mintToken:
          walletName: "issuer"
          amount: 10
      - loop:
          count: $TRANSFER_LOOP_COUNT
          actions:
            - transferToken:
                walletName: "issuer"
                receiverName: "recipient"
                amount: 1
            - transferToken:
                walletName: "recipient"
                receiverName: "issuer"
                amount: 1
            - transferToken:
                walletName: "issuer"
                receiverName: "recipient"
                amount: 2
            - transferToken:
                walletName: "recipient"
                receiverName: "issuer"
                amount: 2
    after:
      unlockWallets:
        wallets: ["issuer", "recipient"]

EOF

echo "✅ YAML written to $OUTFILE"

# Generate new mnemonics for issuers and recipients to announce new tokens
node generate-mnemonics.js -c $VUSERS_COUNT -s 128 -o batch-source-wallets.txt
echo "New source mnemonics generated"

node generate-mnemonics.js -c $VUSERS_COUNT -s 128 -o batch-recipient-wallets.txt
echo "New recipient mnemonics generated"

# Set the config file environment variable
export CONFIG_FILE=$CONFIG_PATH

# Running the generated scenario
echo "Running the Token flow test scenario..."
yarn run artillery run $OUTFILE && rm -rf .artillery-locks-db
echo "Running scenario finished!"
