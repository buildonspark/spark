#!/bin/bash

# Exit immediately if a command exits with a non-zero status
set -e

# Array of identity private keys
PRIV_KEYS=(
    "5eaae81bcf1fd43fbb92432b82dbafc8273bb3287b42cb4cf3c851fcee2212a5"
    "bc0f5b9055c4a88b881d4bb48d95b409cd910fb27c088380f8ecda2150ee8faf"
    "d5043294f686bc1e3337ce4a44801b011adc67524175f27d7adc85d81d6a4545"
    "f2136e83e8dc4090291faaaf5ea21a27581906d8b108ac0eefdaecf4ee86ac99"
    "effe79dc2a911a5a359910cb7782f5cabb3b7cf01e3809f8d323898ffd78e408"
)

# Array of identity public keys
PUB_KEYS=(
    "0322ca18fc489ae25418a0e768273c2c61cabb823edfb14feb891e9bec62016510"
    "0341727a6c41b168f07eb50865ab8c397a53c7eef628ac1020956b705e43b6cb27"
    "0305ab8d485cc752394de4981f8a5ae004f2becfea6f432c9a59d5022d8764f0a6"
    "0352aef4d49439dedd798ac4aef1e7ebef95f569545b647a25338398c1247ffdea"
    "02c05c88cc8fc181b1ba30006df6a4b0597de6490e24514fbdd0266d2b9cd3d0ba"
)

# Number of SOs to run
MAX_SIGNERS=5

# Number of SOs required to sign a transaction
MIN_SIGNERS=3

# Function to create data directory if it doesn't exist
create_data_dir() {
    echo "=== Checking data directory ==="
    if [ ! -d "_data" ]; then
        echo "Creating _data directory..."
        mkdir -p _data
        if [ $? -eq 0 ]; then
            echo "_data directory created successfully"
        else
            echo "Failed to create _data directory"
            exit 1
        fi
    else
        echo "_data directory already exists"
    fi
}

# Function to create next available run folder with db and logs subfolders
create_run_dir() {
    # Redirect all status messages to stderr
    {
        echo "=== Creating new run directory ==="
        count=0
        while [ -d "_data/run_$count" ]; do
            count=$((count + 1))
        done

        run_dir="$(pwd)/_data/run_$count"
        mkdir -p "$run_dir/db" "$run_dir/logs" "$run_dir/bin"

        if [ $? -eq 0 ]; then
            echo "Created run directory: run_$count"
        else
            echo "Failed to create run directory structure"
            exit 1
        fi
    } >&2  # Redirect all output above to stderr

    # Only return the path to stdout
    printf "%s" "$run_dir"
}

# Function to run a single instance of spark-frost-signer
# Function to create and start a tmux session for signers
run_frost_signers_tmux() {
    local run_dir=$1
    echo ""
    echo "=== Starting Frost Signers ==="
    echo "Run directory: $run_dir"
    local session_name="frost-signers"

    # Kill existing session if it exists (properly handled)
    if tmux has-session -t "$session_name" 2>/dev/null; then
        echo "Killing existing session..."
        tmux kill-session -t "$session_name"
    fi

    # Create new tmux session
    tmux new-session -d -s "$session_name"

    # Split the window into 5 panes and run signers
    for i in {0..4}; do
        if [ $i -ne 0 ]; then
            # Split window horizontally for additional panes
            tmux split-window -t "$session_name" -v
            # Arrange panes evenly
            tmux select-layout -t "$session_name" tiled
        fi

        # Construct the command properly with escaped paths
        local log_file="${run_dir}/logs/signer_${i}.log"
        local cmd="cd signer && cargo run --bin spark-frost-signer --release -- -u /tmp/frost_${i}.sock 2>&1 | tee '${log_file}'"
        # Send the command to tmux
        tmux send-keys -t "$session_name" "$cmd" C-m
    done

    echo ""
    echo "================================================"
    echo "Started all signers in tmux session: $session_name"
    echo "To attach to the session: tmux attach -t $session_name"
    echo "To detach from session: Press Ctrl-b then d"
    echo "To kill the session: tmux kill-session -t $session_name"
    echo "================================================"
    echo ""
}

# Function to build the Go operator
build_go_operator() {
    local run_dir=$1
    echo "=== Building Go operator ==="

    cd spark || {
        echo "Failed to enter spark directory" >&2
        return 1
    }

    # Build the operator
    go build -tags=lightspark -o "${run_dir}/bin/operator" ./bin/operator
    build_status=$?

    cd - > /dev/null

    if [ $build_status -eq 0 ]; then
        echo "Go operator built successfully"
        return 0
    else
        echo "Failed to build Go operator" >&2
        return 1
    fi
}

clone_electrs() {
    if [ ! -d "electrs.dev" ]; then
        echo "Cloning mempool/electrs git repo"
        git clone git@github.com:mempool/electrs.git electrs.dev
    fi
}

run_electrs_tmux() {
    local run_dir=$1
    local session_name="electrs"

    clone_electrs

    # Kill existing session if it exists
    if tmux has-session -t "$session_name" 2>/dev/null; then
        echo "Killing existing electrs session..."
        tmux kill-session -t "$session_name"
    fi

    # Create new tmux session
    tmux new-session -d -s "$session_name"
    local log_file="${run_dir}/logs/electrs.log"

    read -r bitcoind_username bitcoind_password <<< "$(parse_bitcoin_config)"

    local cmd="cd electrs.dev && cargo run --release --bin electrs -- -vvvv --network regtest --daemon-dir ${run_dir}/electrs_data --daemon-rpc-addr 0.0.0.0:8332 --cookie ${bitcoind_username}:${bitcoind_password} --http-addr 0.0.0.0:30000 --electrum-rpc-addr 0.0.0.0:50000 --cors \"*\" --jsonrpc-import 2>&1 | tee '${log_file}'"

    tmux send-keys -t "$session_name" "$cmd" C-m

    echo ""
    echo "================================================"
    echo "Started electrs in tmux session: $session_name"
    echo "To attach to the session: tmux attach -t $session_name"
    echo "To detach from session: Press Ctrl-b then d"
    echo "To kill the session: tmux kill-session -t $session_name"
    echo "================================================"
    echo ""
}

check_electrs_ready() {
   local run_dir=$1
   local timeout=30  # Maximum seconds to wait

   echo "Checking electrs startup status..."

   # Start timer
   local start_time=$(date +%s)

   while true; do
       local is_ready=true
       local current_time=$(date +%s)
       local elapsed=$((current_time - start_time))

       # Check if we've exceeded timeout
       if [ $elapsed -gt $timeout ]; then
           echo "Timeout after ${timeout} seconds waiting for electrs"
           return 1
       fi


       local log_file="${run_dir}/logs/electrs.log"

       if [ ! -f "$log_file" ]; then
           is_ready=false
           break
       fi


       # If all log files exist, break the loop
       if $all_ready; then
           echo "Electrs log files created!"
           return 0
       fi

       # Wait a bit before next check
       sleep 1
       echo -n "."  # Show progress
   done
}

# Function to extract values from bitcoin_regtest.conf
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

run_bitcoind_tmux() {
    local run_dir=$1
    local wipe=$2
    local session_name="bitcoind"
    local datadir="$run_dir/bitcoind"


    # Read config values
    read -r bitcoind_username bitcoind_password <<< "$(parse_bitcoin_config)"
    # Ensure data directory exists
    mkdir -p "$datadir"
    cp bitcoin_regtest.conf "$datadir/bitcoin_regtest.conf"

    # Kill existing session if it exists
    if tmux has-session -t "$session_name" 2>/dev/null; then
        echo "Killing existing bitcoind session..."
        tmux kill-session -t "$session_name"
        if [ "$wipe" = true ]; then
            bitcoin-cli -regtest -rpcuser="$bitcoind_username" -rpcpassword="$bitcoind_password" stop
        fi
    fi

    # Create new tmux session
    tmux new-session -d -s "$session_name"

    local log_file="$run_dir/logs/bitcoind.log"
    local cmd="bitcoind -regtest -datadir=$datadir -conf=./bitcoin_regtest.conf -debug=1 2>&1 | tee '$log_file'"

    # Send the command to tmux
    tmux send-keys -t "$session_name" "$cmd" C-m

    echo ""
    echo "================================================"
    echo "Started bitcoind in tmux session: $session_name"
    echo "To attach to the session: tmux attach -t $session_name"
    echo "To detach from session: Press Ctrl-b then d"
    echo "To kill the session: tmux kill-session -t $session_name"
    echo "================================================"
    echo ""
}

# Function to create operator config JSON
create_operator_config() {
    local run_dir=$1
    shift 1 # Remove first argument
    local pub_keys=("$@")  # Get remaining arguments as pub_keys array
    local config_file="${run_dir}/config.json"

    # Create JSON array of operators
    local json="["
    for i in {0..4}; do
        # Add comma if not first item
        if [ $i -ne 0 ]; then
            json+=","
        fi

        # Calculate port
        local port=$((8535 + i))

        json+=$(cat <<EOF
{
    "id": $i,
    "address": "localhost:$port",
    "external_address": "localhost:$port",
    "identity_public_key": "${pub_keys[$i]}",
    "cert_path": "${run_dir}/server_${i}.crt"
}
EOF
        )

    done
    json+="]"

    # Write to file
    echo "$json" > "$config_file"
    echo "Created operator config at: $config_file"
}

# Function to run operators in tmux
run_operators_tmux() {
   local run_dir=$1
   local min_signers=$2
   local session_name="operators"
   local operator_config_file="${run_dir}/config.json"

   # Kill existing session if it exists
   if tmux has-session -t "$session_name" 2>/dev/null; then
       echo "Killing existing session..."
       tmux kill-session -t "$session_name"
   fi

   # Create new tmux session
   tmux new-session -d -s "$session_name"

   # Split the window into 5 panes and run operators
   for i in {0..4}; do
       if [ $i -ne 0 ]; then
           # Split window horizontally for additional panes
           tmux split-window -t "$session_name" -v
           # Arrange panes evenly
           tmux select-layout -t "$session_name" tiled
       fi

       # Calculate port
       local port=$((8535 + i))

       local temp_config_file="temp_config_operator_$i.dev.yaml"

       cp so.template.config.yaml "$temp_config_file"

       # Construct paths
       local log_file="${run_dir}/logs/sparkoperator_${i}.log"
       local db_file="postgresql://127.0.0.1:5432/sparkoperator_${i}?sslmode=disable"
       local signer_socket="unix:///tmp/frost_${i}.sock"

       local priv_key_file="${run_dir}/operator_${i}.key"
       local key_file="${run_dir}/server_${i}.key"
       local cert_file="${run_dir}/server_${i}.crt"

       # Construct the command with all parameters
       local cmd="${run_dir}/bin/operator \
           -config '${temp_config_file}' \
           -index ${i} \
           -key '${priv_key_file}' \
           -operators '${operator_config_file}' \
           -threshold ${min_signers} \
           -signer '${signer_socket}' \
           -port ${port} \
           -database '${db_file}' \
           -server-cert '${cert_file}' \
           -server-key '${key_file}' \
           ${cert_config} \
           -run-dir '${run_dir}' \
           -local true \
           2>&1 | tee '${log_file}'"

       # Send the command to tmux
       tmux send-keys -t "$session_name" "$cmd" C-m
   done

   echo ""
   echo "================================================"
   echo "Started all operators in tmux session: $session_name"
   echo "To attach to the session: tmux attach -t $session_name"
   echo "To detach from session: Press Ctrl-b then d"
   echo "To kill the session: tmux kill-session -t $session_name"
   echo "================================================"
   echo ""
}

# Function to check if operators are running by checking log file existence
check_operators_ready() {
   local run_dir=$1
   local timeout=30  # Maximum seconds to wait

   echo "Checking operators startup status..."

   # Start timer
   local start_time=$(date +%s)

   while true; do
       local all_ready=true
       local current_time=$(date +%s)
       local elapsed=$((current_time - start_time))

       # Check if we've exceeded timeout
       if [ $elapsed -gt $timeout ]; then
           echo "Timeout after ${timeout} seconds waiting for operators"
           return 1
       fi

       # Check each operator's log file existence
       for i in {0..4}; do
           local log_file="${run_dir}/logs/sparkoperator_${i}.log"

           if [ ! -f "$log_file" ]; then
               all_ready=false
               break
           fi
       done

       # If all log files exist, break the loop
       if $all_ready; then
           echo "All operator log files created!"
           return 0
       fi

       # Wait a bit before next check
       sleep 1
       echo -n "."  # Show progress
   done
}

# Function to check if signers are running by checking log file existence
check_signers_ready() {
   local run_dir=$1
   local timeout=30  # Maximum seconds to wait

   echo "Checking signers startup status..."

   # Start timer
   local start_time=$(date +%s)

   while true; do
       local all_ready=true
       local current_time=$(date +%s)
       local elapsed=$((current_time - start_time))

       # Check if we've exceeded timeout
       if [ $elapsed -gt $timeout ]; then
           echo "Timeout after ${timeout} seconds waiting for signers"
           return 1
       fi

       # Check each signer's log file existence
       for i in {0..4}; do
           local log_file="${run_dir}/logs/signer_${i}.log"

           if [ ! -f "$log_file" ]; then
               all_ready=false
               break
           fi
       done

       # If all log files exist, break the loop
       if $all_ready; then
           echo "All signer log files created!"
           return 0
       fi

       # Wait a bit before next check
       sleep 1
       echo -n "."  # Show progress
   done
}

reset_databases() {
    local force_reset=$1
    local max_count=4  # Optional parameter for number of DBs, defaults to 5

    if [ "$force_reset" = true ]; then
        echo "Force reset: dropping and recreating all databases (0 to $max_count)..."

        # Terminate all relevant connections first
        for i in $(seq 0 $max_count); do
            db="sparkoperator_$i"
            psql postgres -c "
            SELECT pg_terminate_backend(pid)
            FROM pg_stat_activity
            WHERE datname = '$db'
            AND pid <> pg_backend_pid();" > /dev/null 2>&1
        done

        # Drop and recreate
        for i in $(seq 0 $max_count); do
            db="sparkoperator_$i"
            echo "Resetting $db..."
            dropdb --if-exists "$db" > /dev/null 2>&1
            createdb "$db" > /dev/null 2>&1
        done
    else
        echo "Soft reset: creating databases only if they don't exist (0 to $max_count)..."

        for i in $(seq 0 $max_count); do
            db="sparkoperator_$i"
            if ! psql -lqt | cut -d \| -f 1 | grep -qw "$db"; then
                echo "Creating $db as it doesn't exist..."
                createdb "$db" > /dev/null 2>&1
            fi
        done
    fi

    cd spark
    for i in $(seq 0 $max_count); do
        db="sparkoperator_$i"
        atlas migrate apply --dir "file://so/ent/migrate/migrations" --url "postgresql://127.0.0.1:5432/$db?sslmode=disable"
    done
    cd -

    echo "Database operation complete!"
}

create_private_key_files() {
    local run_dir=$1
    local priv_keys=("${@:2}")

    for i in {0..4}; do
        local priv_key="${priv_keys[$i]}"
        local file="${run_dir}/operator_${i}.key"
        echo "$priv_key" > "$file"

        local key_file="${run_dir}/server_${i}.key"
        openssl genrsa -out "$key_file" 2048

        local cert_file="${run_dir}/server_${i}.crt"
        openssl req -new -x509 -key "$key_file" -out "$cert_file" -days 365 -subj "/CN=localhost" -addext "subjectAltName = DNS:localhost"
    done
}

# Initialize flags
WIPE=false
TLS=true

# Parse command line arguments
for arg in "$@"; do
    case $arg in
        --wipe)
            WIPE=true
            shift
            ;;
    esac
done

# Call reset_databases based on wipe flag
reset_databases $WIPE

create_data_dir
run_dir=$(create_run_dir)
echo "Working with directory: $run_dir"

run_bitcoind_tmux "$run_dir" $WIPE

run_electrs_tmux "$run_dir"

if ! check_electrs_ready "$run_dir"; then
    echo "Failed to start electrs"
    exit 1
fi

# For all 5 instances
run_frost_signers_tmux "$run_dir"

# Build SOs
build_go_operator "$run_dir" || {
    echo "Build failed, exiting"
    exit 1
}

# Create operator config
create_operator_config "$run_dir" "${PUB_KEYS[@]}"
create_private_key_files "$run_dir" "${PRIV_KEYS[@]}"

if ! check_signers_ready "$run_dir"; then
    echo "Failed to start all signers"
    exit 1
fi

echo "All signers are ready"

# Run operators
run_operators_tmux "$run_dir" "$MIN_SIGNERS"

if ! check_operators_ready "$run_dir"; then
    echo "Failed to start all operators"
    exit 1
fi
