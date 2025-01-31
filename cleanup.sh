#!/bin/bash

tmux kill-session -t frost-signers
tmux kill-session -t operators
tmux kill-session -t lrcd
tmux kill-session -t bitcoind

# Terminate all relevant connections first
for i in $(seq 0 4); do
    db="operator_$i"
    psql postgres -c "
    SELECT pg_terminate_backend(pid) 
    FROM pg_stat_activity 
    WHERE datname = '$db' 
    AND pid <> pg_backend_pid();" > /dev/null 2>&1
done

# Drop and recreate
for i in $(seq 0 4); do
    db="operator_$i"
    echo "Resetting $db..."
    dropdb --if-exists "$db" > /dev/null 2>&1
    createdb "$db" > /dev/null 2>&1
done
rm -rf _data
rm -rf temp_config_*
