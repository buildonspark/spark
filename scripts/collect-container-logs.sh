#!/bin/bash

LOG_DIR="${LOG_DIR:-logs}"
mkdir -p "$LOG_DIR"

# Define service configurations
declare -a spark_config=("spark" "spark" "operator" "signer" "atlas")
declare -a yuv_config=("yuv" "yuvd" "yuvd" "sqlx")
declare -a bitcoin_config=("bitcoin" "bitcoind" "bitcoind")
declare -a electrs_config=("bitcoin" "electrs" "electrs")
declare -a mempool_config=("bitcoin" "mempool" "backend" "frontend")

# Function to collect logs for a namespace, app and containers
collect_logs() {
  local namespace=$1
  local app_name=$2
  local containers=("${@:3}")

  # Add context flag if context is specified
  local context_flag=""
  if [ -n "$context" ]; then
    context_flag="--context $context"
  fi

  pods=$(kubectl $context_flag -n "$namespace" get pods -l "app.kubernetes.io/name=$app_name" -o jsonpath='{.items[*].metadata.name}')

  for pod in $pods; do
    for container in "${containers[@]}"; do
      output_file="${LOG_DIR}/${pod}.${container}.log"
      echo "Collecting logs for $namespace/$pod/$container..."
      kubectl $context_flag -n "$namespace" logs "$pod" -c "$container" > "$output_file"
    done
  done
}

# Default behavior is to collect specific logs
collect_all=false
context=""

# Parse command line options
while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)
      collect_spark=true
      collect_yuv=true
      collect_bitcoin=true
      shift
      ;;
    --spark)
      collect_spark=true
      shift
      ;;
    --yuv)
      collect_yuv=true
      shift
      ;;
    --bitcoin)
      collect_bitcoin=true
      shift
      ;;
    --context)
      if [[ -n "$2" && "$2" != --* ]]; then
        context="$2"
        shift 2
      else
        echo "Error: --context requires an argument"
        exit 1
      fi
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [--all] [--spark] [--yuv] [--bitcoin] [--context <context-name>]"
      exit 1
      ;;
  esac
done

# Function to collect logs for a specific service
collect_service_logs() {
  local service_name=$1
  local config_var="${service_name}_config[@]"
  local config=("${!config_var}")
  
  if [ ${#config[@]} -eq 0 ]; then
    echo "Unknown service: $service_name"
    return 1
  fi
  
  local namespace=${config[0]}
  local app_name=${config[1]}
  local containers=("${config[@]:2}")
  
  echo "Collecting $service_name logs..."
  collect_logs "$namespace" "$app_name" "${containers[@]}"
}

# Collect logs based on the flag
collect_requested_logs() {
  if [ "${collect_spark:-false}" = true ] || [ "${collect_yuv:-false}" = true ] || [ "${collect_bitcoin:-false}" = true ]; then
    # Collect specific namespace logs based on flags
    if [ "${collect_spark:-false}" = true ]; then
      collect_service_logs "spark"
    fi
    
    if [ "${collect_yuv:-false}" = true ]; then
      collect_service_logs "yuv"
    fi
    
    if [ "${collect_bitcoin:-false}" = true ]; then
      collect_service_logs "bitcoin"
      collect_service_logs "electrs"
      collect_service_logs "mempool"
    fi
  else
    collect_service_logs "spark"
  fi
}

collect_requested_logs

echo "Log collection complete. Logs saved to $LOG_DIR directory."
