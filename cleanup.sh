#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

parse_bitcoin_config() {
    local config_file="bitcoin_regtest.conf"
    local rpcuser=""
    local rpcpassword=""

    while IFS='=' read -r key value; do
        # Remove leading/trailing whitespace
        key=$(echo "$key" | tr -d '[:space:]')
        value=$(echo "$value" | tr -d '[:space:]')

        case "$key" in
            "rpcuser") rpcuser="$value" ;;
            "rpcpassword") rpcpassword="$value" ;;
        esac
    done < "$config_file"

    echo "$rpcuser $rpcpassword"
}

tmux kill-session -t frost-signers
tmux kill-session -t operators
tmux kill-session -t lrcd
tmux kill-session -t electrs
tmux kill-session -t bitcoind

read -r bitcoind_username bitcoind_password <<< "$(parse_bitcoin_config)"
bitcoin-cli -regtest -rpcuser="$bitcoind_username" -rpcpassword="$bitcoind_password" --conf="$SCRIPT_DIR/bitcoin_regtest.conf" stop

# Terminate all relevant connections first
for i in $(seq 0 4); do
    db="sparkoperator_$i"
    psql postgres -c "
    SELECT pg_terminate_backend(pid) 
    FROM pg_stat_activity 
    WHERE datname = '$db' 
    AND pid <> pg_backend_pid();" > /dev/null 2>&1

    db="lrc20_$i"
    psql postgres -c "
    SELECT pg_terminate_backend(pid)
    FROM pg_stat_activity
    WHERE datname = '$db'
    AND pid <> pg_backend_pid();" > /dev/null 2>&1
done

# Drop and recreate
for i in $(seq 0 4); do
    db="sparkoperator_$i"
    echo "Resetting $db..."
    dropdb --if-exists "$db" > /dev/null 2>&1
    createdb "$db" > /dev/null 2>&1

    db="lrc20_$i"
    echo "Resetting $db..."
    dropdb --if-exists "$db" > /dev/null 2>&1
    createdb "$db" > /dev/null 2>&1
done
rm -rf _data
rm -rf temp_config_*
rm -rf lrc20.dev/lrc20
rm -rf electrs.dev/electrs_data
rm -rf electrs.dev/db
