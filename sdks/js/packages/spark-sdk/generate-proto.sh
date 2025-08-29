#!/usr/bin/env bash
set -euo pipefail

# Always run from the package root (this script's directory)
cd "$(dirname "$0")"

# Ensure protoc plugins in local node_modules are discoverable
export PATH="$PATH:./node_modules/.bin:../../node_modules/.bin"

# Prefer explicitly pointing protoc at the ts-proto plugin to avoid PATH issues
PLUGIN_PATH="$(pwd)/node_modules/.bin/protoc-gen-ts_proto"
if [[ ! -x "$PLUGIN_PATH" ]]; then
  # If the file exists but isn't marked executable (common on some systems), try node directly
  if [[ -f "$PLUGIN_PATH" ]]; then
    chmod +x "$PLUGIN_PATH" || true
  fi
fi

echo "[generate-proto] Running protoc..."
# Hardcoded protoc arguments
ARGS=(
  "--ts_proto_out=./src/proto"
  "--ts_proto_opt=outputServices=nice-grpc,useExactTypes=false,outputServices=generic-definitions,oneof=unions,importSuffix=.js"
  "--descriptor_set_out=./src/spark_descriptors.pb"
  "--include_imports"
  "--proto_path=../../../../protos"
  "spark.proto"
  "spark_token.proto"
  "mock.proto"
  "spark_authn.proto"
)

if [[ -x "$PLUGIN_PATH" ]]; then
  ARGS+=("--plugin=protoc-gen-ts_proto=$PLUGIN_PATH")
fi

protoc "${ARGS[@]}"

echo "[generate-proto] Embedding descriptors..."
node scripts/embed-descriptors.mjs

echo "[generate-proto] Formatting generated code..."
yarn format:fix

echo "[generate-proto] Done."


