#!/bin/bash
set -e

init_pid_tracking() {
    if [ -z "$PID_TRACKING_INITIALIZED" ]; then
        if [ -z "$PID_TRACKING_FILE" ]; then
            PID_TRACKING_FILE="/tmp/port_forward_pids_$(date +%s)_$$.txt"
        fi
        echo "Initializing PID tracking with file: $PID_TRACKING_FILE"
        touch "$PID_TRACKING_FILE"
        export PID_TRACKING_FILE
        export PID_TRACKING_INITIALIZED=1
    else
        echo "PID tracking already initialized with file: $PID_TRACKING_FILE"
    fi
}

save_pid() {
    local pid=$1
    echo "$pid" >> "$PID_TRACKING_FILE"
}

setup_port_forward() {
    local namespace=$1
    local target=$2
    local local_port=$3
    local remote_port=$4

    kubectl -n $namespace port-forward $target $local_port:$remote_port 2>&1 | grep -v "Forwarding from" &
    local pid=$!
    save_pid $pid
    echo "Started port-forward localhost:$local_port -> $target.$namespace:$remote_port (PID: $pid)"
}

cleanup_port_forwards() {
    echo "Cleaning up port-forwards..."
    if [ -f "$PID_TRACKING_FILE" ]; then
        IFS=$'\n' read -d '' -r -a cleanup_pids < "$PID_TRACKING_FILE" || true

        for pid in "${cleanup_pids[@]}"; do
            echo "Killing port-forward PID: $pid"
            kill $pid 2>/dev/null || true
        done

        rm -f "$PID_TRACKING_FILE"
    fi
}

init_pid_tracking
