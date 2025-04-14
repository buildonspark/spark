#!/bin/bash
set -e

NAMESPACE="test-signer"
POD_NAME="frost-signer"
SPARK_TAG=${SPARK_TAG:-latest}
SPARK_REPO="674966927423.dkr.ecr.us-west-2.amazonaws.com/spark-go"
PORT=9999
LISTEN_ADDRESS="localhost:${PORT}"
SECRET_NAME="ecr"

source "$(dirname "$0")/port-forwarding.sh"

# Use minikube's docker environment for local images
if [ "$USE_DEV_SPARK" = "true" ]; then
    echo "Using minikube docker environment for dev builds..."
    eval "$(minikube docker-env)"

    # Verify dev images exist
    if ! docker image inspect spark:dev >/dev/null 2>&1; then
        echo "Error: spark:dev image not found. Please run build.sh first."
        exit 1
    fi
    echo "Using local spark:dev image"
    SPARK_REPO="spark"
    SPARK_TAG="dev"
else
    echo "Using remote spark image: ${SPARK_REPO:-default}:${SPARK_TAG:-latest}"
fi

# Create namespace if it doesn't exist
if ! kubectl get namespace "$NAMESPACE" &>/dev/null; then
    echo "Creating namespace $NAMESPACE..."
    kubectl create namespace "$NAMESPACE"
fi

source "$(dirname "$0")/ecr-secret.sh"
create_ecr_secret "$NAMESPACE"

echo "Deploying frost signer pod..."
kubectl run "$POD_NAME" \
    --image="$SPARK_REPO:$SPARK_TAG" \
    --namespace="$NAMESPACE" \
    --restart=Never \
    --port=$PORT \
    --overrides="{\"spec\": {\"imagePullSecrets\": [{\"name\": \"$SECRET_NAME\"}]}}" \
    --command -- spark-frost-signer --port $PORT

# Wait for pod to be ready
echo "Waiting for signer pod to be ready..."
kubectl wait -n "$NAMESPACE" --for=condition=Ready pod/"$POD_NAME" --timeout=60s

sleep 2

setup_port_forward "$NAMESPACE" pod/"$POD_NAME" $PORT $PORT

sleep 2

echo "Signer successfully deployed and available at $LISTEN_ADDRESS"