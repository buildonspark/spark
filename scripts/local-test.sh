#!/bin/bash
#
# local-test.sh - Script to run Spark tests in a hermetic minikube environment
#
# This script sets up a local minikube environment for running Spark hermetictests,
# including deploying necessary services and setting up port forwarding.
#
# Usage:
#   ./scripts/local-test.sh [--dev-spark] [--dev-lrc20] [--keep-data]
#
# Options:
#   --dev-spark         - Sets USE_DEV_SPARK=true to use the locally built dev spark image
#   --dev-lrc20         - Sets USE_DEV_LRC20=true to use the locally built dev lrc20 image
#   --keep-data         - Sets RESET_DBS=false to preserve existing test data (databases and blockchain)
#
# Environment Variables:
#   RESET_DBS           - Whether to reset operator databases and bitcoin blockchain (default: true)
#   USE_DEV_SPARK       - Whether to use the dev spark image built into minikube (default: false)
#   USE_DEV_LRC20       - Whether to use the dev lrc20 image built into minikube (default: false)
#   SPARK_TAG           - Image tag to use for both Spark operator and signer (default: latest)
#   LRC20_TAG           - Image tag to use for LRC20 (default: latest)
#   USE_LIGHTSPARK_HELM_REPO - Whether to fetch helm charts from remote repo (default: false)
#   OPS_DIR             - Path to the Lightspark ops repository which contains helm charts (auto-detected if not set)
#
# Example:
#   # Run with default settings
#   ./scripts/local-test.sh
#
#   # Run with dev spark image and keep existing test data
#   ./scripts/local-test.sh --dev-spark --keep-data
#
#   # Run with custom environment variables
#   SPARK_TAG=v1.0.0 ./scripts/local-test.sh

set -e

: "${USE_DEV_SPARK:=false}"
: "${USE_DEV_LRC20:=false}"
: "${RESET_DBS:=true}"

while [[ $# -gt 0 ]]; do
    case $1 in
        --dev-spark)
            USE_DEV_SPARK=true
            shift
            ;;
        --dev-lrc20)
            USE_DEV_LRC20=true
            shift
            ;;
        --keep-data)
            RESET_DBS=false
            shift
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

export USE_DEV_SPARK
export RESET_DBS

HERMETIC_TEST_FILE="/tmp/spark_hermetic"
MINIKUBE_CA_FILE="/tmp/minikube-ca.pem"

source "$(dirname "$0")/port-forwarding.sh"

cleanup() {
    rm -f "$HERMETIC_TEST_FILE"
    rm -f "$MINIKUBE_CA_FILE"
    cleanup_port_forwards
}

trap cleanup EXIT

check_minikube_setup() {
    if ! kubectl get namespace cert-manager >/dev/null 2>&1 || \
       ! kubectl get service postgres >/dev/null 2>&1; then
        echo "Error: Minikube environment not properly set up"
        echo "Please run: ops/minikube/setup.sh"
        echo "For more information, see: https://github.com/lightsparkdev/ops/blob/main/minikube/README.md"
        exit 1
    fi
}

cleanup_k8s() {
    echo "Cleaning up previous deployments..."
    helm uninstall -n spark regtest --ignore-not-found 2>/dev/null || true
    helm uninstall -n lrc20 regtest --ignore-not-found 2>/dev/null || true
    helm uninstall -n bitcoin regtest --ignore-not-found 2>/dev/null || true
    helm uninstall -n bitcoin regtest-mempool --ignore-not-found 2>/dev/null || true
    helm uninstall -n bitcoin regtest-electrs --ignore-not-found 2>/dev/null || true


    kubectl delete namespace spark --ignore-not-found &
    kubectl delete namespace lrc20 --ignore-not-found &
    kubectl delete namespace test-signer --ignore-not-found &

    kubectl wait --for=delete namespace/spark --timeout=60s 2>/dev/null &
    WAIT_SPARK_PID=$!
    kubectl wait --for=delete namespace/lrc20 --timeout=60s 2>/dev/null &
    WAIT_LRC20_PID=$!
    kubectl wait --for=delete namespace/test-signer --timeout=60s 2>/dev/null &
    WAIT_TEST_SIGNER_PID=$!

    wait_pids=("$WAIT_SPARK_PID" "$WAIT_LRC20_PID" "$WAIT_TEST_SIGNER_PID")

    if [ "$RESET_DBS" = "true" ]; then
        kubectl delete namespace bitcoin --ignore-not-found &
        kubectl wait --for=delete namespace/bitcoin --timeout=60s 2>/dev/null &
        WAIT_BITCOIN_SPARK_PID=$!
        wait_pids+=("$WAIT_BITCOIN_SPARK_PID")
    fi

    wait "${wait_pids[@]}" || true
}

# Find ops directory if not specified in environment
if [ -z "$OPS_DIR" ]; then
    for path in "./ops" "../ops" "../../ops"; do
        if [ -d "$path" ]; then
            OPS_DIR=$(cd "$path" && pwd)  # Get absolute path
            export OPS_DIR
            break
        fi
    done
fi

# Verify ops directory exists
if [ ! -d "$OPS_DIR" ]; then
    echo "Error: ops directory not found in ./ops, ../ops, or ../../ops"
    echo "Please either:"
    echo "1. Run this script from a directory with access to the ops repo"
    echo "2. Set OPS_DIR environment variable to point to the ops repo location"
    exit 1
fi

echo "Using ops directory at: $OPS_DIR"

# Start minikube if not running
if ! minikube status > /dev/null 2>&1; then
    echo "Error: Minikube is not running"
    echo "Please start minikube using: $OPS_DIR/minikube/setup.sh"
    exit 1
fi

check_minikube_setup
cleanup_k8s

./scripts/minikube-deploy-spark-services.sh
setup_port_forward default pod/postgres-0 15432 5432

touch "$HERMETIC_TEST_FILE"

"$(dirname "$0")/run-local-signer-container.sh" &
"$(dirname "$0")/export-minikube-ca.sh"
echo "Waiting for 30 seconds for services to all stabilize before running development DKG..."
sleep 30
echo "Done!"
"$(dirname "$0")/run-development-dkg.sh"

echo "Run your tests now (go test ./so/grpc_test/... or gotestsum --format testname ./so/grpc_test/... ). Ctrl-C when done."
while true; do
    sleep 10
done

# Run tests
# go test ./so/grpc_test/...
