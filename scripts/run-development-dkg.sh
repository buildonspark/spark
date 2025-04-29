#!/bin/bash

# This script is used to run the DKG setup in a development integration test
# environment (e.g., run-everything.sh or minikube)

cd "$(dirname "$0")/../spark" || { echo "Failed to cd to spark directory"; exit 1; }

echo "Running DKG setup..."
DKG_OUTPUT="$(go test -v -count 1 ./so/grpc_test -run "^TestDKG$")"
if grep "PASS: TestDKG" <<< "$DKG_OUTPUT"; then
    echo "DKG setup completed successfully"
else
    echo "DKG setup failed"
    exit 1
fi
