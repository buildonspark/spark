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
        local cmd="cd spark-frost-signer && cargo run --release -- -u /tmp/frost_${i}.sock 2>&1 | tee '${log_file}'"
        # Send the command to tmux
        tmux send-keys -t "$session_name.$i" "$cmd" C-m
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
    go build -o "${run_dir}/bin/operator" bin/operator/main.go
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

# Function to create operator config JSON
create_operator_config() {
    local run_dir=$1
    shift  # Remove first argument
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
        
        # Add operator entry
        json+=$(cat <<EOF
{
    "id": $i,
    "address": "localhost:$port",
    "identity_public_key": "${pub_keys[$i]}"
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
   local priv_keys=("${@:3}")  # Get private keys array from remaining args
   local session_name="operators"
   local config_file="${run_dir}/config.json"
   
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
       
       # Construct paths
       local log_file="${run_dir}/logs/operator_${i}.log"
       local db_file="${run_dir}/db/operator_${i}.sqlite"
       local signer_socket="unix:///tmp/frost_${i}.sock"
       
       # Construct the command with all parameters
       local cmd="${run_dir}/bin/operator \
           -index ${i} \
           -key '${priv_keys[$i]}' \
           -operators '${config_file}' \
           -threshold ${min_signers} \
           -signer '${signer_socket}' \
           -port ${port} \
           -database '${db_file}' \
           2>&1 | tee '${log_file}'"
       
       # Send the command to tmux
       tmux send-keys -t "$session_name.$i" "$cmd" C-m
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
           local log_file="${run_dir}/logs/operator_${i}.log"
           
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

create_data_dir
run_dir=$(create_run_dir)
echo "Working with directory: $run_dir"

# For all 5 instances
run_frost_signers_tmux "$run_dir"

# Build SOs
build_go_operator "$run_dir" || {
    echo "Build failed, exiting"
    exit 1
}

# Create operator config
create_operator_config "$run_dir" "${PUB_KEYS[@]}"

if ! check_signers_ready "$run_dir"; then
    echo "Failed to start all signers"
    exit 1
fi

# Run operators
run_operators_tmux "$run_dir" "$MIN_SIGNERS" "${PRIV_KEYS[@]}"

if ! check_operators_ready "$run_dir"; then
    echo "Failed to start all operators"
    exit 1
fi
