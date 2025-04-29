#!/bin/bash

# This script serves as an integration test to test CJS paths directly and ensure package compatibility

set -e

# Recursive function to kill a process tree using pgrep
kill_tree() {
  local pid=$1
  # Recursively kill child processes first
  for child in $(pgrep -P "$pid" 2>/dev/null); do
    kill_tree "$child"
  done
  # Attempt to kill the parent process; ignore errors if it fails
  kill "$pid" 2>/dev/null || true
}

cleanup() {
  if [ -n "$SERVER_PID" ]; then
    echo "Stopping the server and its child processes..."
    kill_tree "$SERVER_PID"
    # Wait for the process to exit; ignore errors if it's already gone
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# Fail early if port 3000 is in use
if lsof -Pi :3000 -sTCP:LISTEN -t >/dev/null; then
  echo "Error: Port 3000 is already in use. Aborting test."
  exit 1
fi

# Start the server in the background
yarn run start &
SERVER_PID=$!

# Wait for the server's endpoint to be ready (15-second timeout)
if ! yarn wait-on http://localhost:3000 --timeout 15000; then
  echo "Server did not start within timeout."
  exit 1
fi

# Check that the server process is still running
if ! kill -0 "$SERVER_PID" 2>/dev/null; then
  echo "Server process exited unexpectedly."
  exit 1
fi

# Send a test HTTP request to /create-spark-wallet
HTTP_STATUS_CREATE_WALLET=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:3000/create-spark-wallet)
CREATE_WALLET_RESPONSE=$(curl -s http://localhost:3000/create-spark-wallet)
echo "Response from /create-spark-wallet: $CREATE_WALLET_RESPONSE"

# Send a test HTTP request to /test-wasm
HTTP_STATUS_TEST_WASM=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:3000/test-wasm)
TEST_WASM_RESPONSE=$(curl -s http://localhost:3000/test-wasm)
echo "Response from /test-wasm: $TEST_WASM_RESPONSE"

# Check that both endpoints return HTTP 200
if [ "$HTTP_STATUS_CREATE_WALLET" -eq 200 ] && [ "$HTTP_STATUS_TEST_WASM" -eq 200 ]; then
  echo "Test passed: both endpoints returned HTTP 200"
  exit 0
else
  echo "Test failed:"
  echo "  /create-spark-wallet returned HTTP status $HTTP_STATUS_CREATE_WALLET"
  echo "  /test-wasm returned HTTP status $HTTP_STATUS_TEST_WASM"
  exit 1
fi