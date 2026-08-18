#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SDK_DIR="$SCRIPT_DIR/../../sdks/js/packages/spark-sdk"

cd "$SCRIPT_DIR"
wasm-pack build --target nodejs --out-dir "$SDK_DIR/wasm/nodejs" --out-name wasm_nodejs --no-pack
wasm-pack build --target web --out-dir "$SDK_DIR/wasm/browser" --out-name wasm_browser --no-pack
rm -f "$SDK_DIR/wasm/nodejs/.gitignore" "$SDK_DIR/wasm/browser/.gitignore"

cd "$SDK_DIR"
node --test \
  ./wasm/patch-wasm-browser.test.mjs \
  ./wasm/patch-wasm-nodejs.test.mjs
node ./wasm/patch-wasm-browser.mjs
node ./wasm/patch-wasm-nodejs.mjs
node --check ./src/spark-bindings/wasm/wasm-nodejs.js
