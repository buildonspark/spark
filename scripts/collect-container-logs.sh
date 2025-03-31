#!/bin/bash

LOG_DIR="${LOG_DIR:-logs}"
mkdir -p "$LOG_DIR"

# Function to collect logs for a namespace, app and containers
collect_logs() {
  local namespace=$1
  local app_name=$2
  local containers=("${@:3}")

  pods=$(kubectl -n "$namespace" get pods -l "app.kubernetes.io/name=$app_name" -o jsonpath='{.items[*].metadata.name}')

  for pod in $pods; do
    for container in "${containers[@]}"; do
      output_file="${LOG_DIR}/${pod}.${container}.log"
      kubectl -n "$namespace" logs "$pod" -c "$container" > "$output_file"
    done
  done
}
