#!/bin/bash
set -e

eval "$(minikube docker-env)"
echo "Building images in minikube's docker environment..."

echo "Building spark image..."
repo_root=$(git rev-parse --show-toplevel)
docker build -t spark:dev "$repo_root"
echo "Successfully built spark:dev"

echo -e "\nAvailable images in minikube:"
docker images | grep -E "^(spark)\s+"

echo -e "\nNote: To interact with these images in your terminal, run:"
echo "  eval \$(minikube docker-env)"
echo "To revert back to your local docker:"
echo "  eval \$(minikube docker-env -u)"
