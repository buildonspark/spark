#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SDK_DIR="$SCRIPT_DIR/../../sdks/js/packages/spark-sdk"

cd "$SCRIPT_DIR"
wasm-pack build --target nodejs --out-dir "$SDK_DIR/wasm/token-primitives/nodejs" --out-name wasm_token_primitives_nodejs --no-pack
wasm-pack build --target web --out-dir "$SDK_DIR/wasm/token-primitives/browser" --out-name wasm_token_primitives_browser --no-pack
rm -f \
  "$SDK_DIR/wasm/token-primitives/nodejs/.gitignore" \
  "$SDK_DIR/wasm/token-primitives/browser/.gitignore"

cd "$SDK_DIR"
node ./wasm/patch-token-primitives-wasm-browser.mjs
node ./wasm/patch-token-primitives-wasm-nodejs.mjs
node --check ./src/token-primitives-bindings/wasm/wasm-nodejs.js
