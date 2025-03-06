#!/bin/bash

network="regtest"
if [ "$1" == "mainnet" ]; then
    network="mainnet"
fi

cmd="cd signer && cargo run --bin spark-frost-signer --release -- -u /tmp/frost_wallet.sock"

session_name="wallet-signers"

# Kill existing session if it exists (properly handled)
if tmux has-session -t "$session_name" 2>/dev/null; then
    echo "Killing existing session..."
    tmux kill-session -t "$session_name"
fi

# Create a new tmux session
tmux new-session -d -s "$session_name"
tmux split-window -t "$session_name" -v
tmux send-keys -t "$session_name" "$cmd" C-m

cd spark && go run bin/user_wallet/main.go $network
