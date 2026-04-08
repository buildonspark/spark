#!/bin/sh
set -e

INDEX="${1:?Usage: entrypoint.sh <operator-index>}"

SOCKET_PATH="/tmp/frost_${INDEX}.sock"
KEY_DIR="/opt/spark/keys"
TLS_DIR="/opt/spark/tls"

echo "Running atlas migrations for operator $INDEX..."
atlas migrate apply \
  --dir "file:///opt/spark/migrations" \
  --url "postgresql://postgres@postgres:5432/sparkoperator_${INDEX}?sslmode=disable"
atlas migrate apply \
  --dir "file:///opt/spark/ephemeral_migrations" \
  --url "postgresql://postgres@postgres:5432/spark_ephemeral_${INDEX}?sslmode=disable"

echo "Starting frost signer on $SOCKET_PATH..."
spark-frost-signer -u "$SOCKET_PATH" &
SIGNER_PID=$!

# Wait for the socket to appear
for attempt in $(seq 1 30); do
  if [ -S "$SOCKET_PATH" ]; then
    echo "Frost signer ready"
    break
  fi
  if ! kill -0 "$SIGNER_PID" 2>/dev/null; then
    echo "Frost signer exited unexpectedly"
    exit 1
  fi
  sleep 1
done

if [ ! -S "$SOCKET_PATH" ]; then
  echo "Timed out waiting for frost signer socket"
  exit 1
fi

echo "Starting spark operator $INDEX..."
exec spark-operator \
  -config /opt/spark/operator.config.yaml \
  -index "$INDEX" \
  -key "$KEY_DIR/operator_${INDEX}.key" \
  -operators /opt/spark/config.json \
  -threshold 2 \
  -signer "unix://$SOCKET_PATH" \
  -port 8535 \
  -database "postgresql://postgres@postgres:5432/sparkoperator_${INDEX}?sslmode=disable" \
  -ephemeral-database "postgresql://postgres@postgres:5432/spark_ephemeral_${INDEX}?sslmode=disable" \
  -server-cert "$TLS_DIR/server_${INDEX}.crt" \
  -server-key "$TLS_DIR/server_${INDEX}.key" \
  -local true
