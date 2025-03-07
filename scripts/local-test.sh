#!/bin/bash
set -e

# Forces the DBs to be recreated
: "${RESET_DBS:=true}"
HERMETIC_TEST_FILE="/tmp/spark_hermetic"
MINIKUBE_CA_FILE="/tmp/minikube-ca.pem"
SIGNER_SCRIPT="$(dirname "$0")/run-local-signer-container.sh"

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
    helm uninstall -n bitcoin-spark regtest --ignore-not-found 2>/dev/null || true

    kubectl delete namespace spark --ignore-not-found &
    kubectl delete namespace lrc20 --ignore-not-found &
    kubectl delete namespace test-signer --ignore-not-found &

    kubectl wait --for=delete namespace/spark --timeout=60s 2>/dev/null &
    WAIT_SPARK_PID=$!
    kubectl wait --for=delete namespace/lrc20 --timeout=60s 2>/dev/null &
    WAIT_LRC20_PID=$!
    kubectl wait --for=delete namespace/test-signer --timeout=60s 2>/dev/null &
    WAIT_TEST_SIGNER_PID=$!

    wait_pids=($WAIT_SPARK_PID $WAIT_LRC20_PID $WAIT_TEST_SIGNER_PID)

    if [ "$RESET_DBS" = "true" ]; then
        kubectl delete namespace bitcoin-spark --ignore-not-found &
        kubectl wait --for=delete namespace/bitcoin-spark --timeout=60s 2>/dev/null &
        WAIT_BITCOIN_SPARK_PID=$!
        wait_pids+=($WAIT_BITCOIN_SPARK_PID)
    fi

    wait "${wait_pids[@]}" || true
}

# Find ops directory if not specified in environment
if [ -z "$OPS_DIR" ]; then
    for path in "./ops" "../ops" "../../ops"; do
        if [ -d "$path" ]; then
            OPS_DIR=$(cd "$path" && pwd)  # Get absolute path
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

OPS_DIR=$OPS_DIR ./scripts/minikube-deploy-spark-services.sh
setup_port_forward default pod/postgres-0 15432 5432

touch "$HERMETIC_TEST_FILE"

# Start the local signer
"$SIGNER_SCRIPT" &
SIGNER_SCRIPT_PID=$!

kubectl get configmap cluster-ca --template='{{index .data "ca.crt"}}' > "$MINIKUBE_CA_FILE"

echo "Run your tests now (go test ./so/grpc_test/...). Ctrl-C when done."
while true; do
    sleep 10
done

# Run tests
# go test ./so/grpc_test/...
