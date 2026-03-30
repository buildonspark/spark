#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/../.."

echo "Generating python bindings..."
cargo run --bin uniffi-bindgen generate src/spark_token_primitives.udl --language python --out-dir spark-token-primitives-python/src/spark_token_primitives/ --no-format

echo "Building native library..."
cargo build --profile release-smaller

echo "Copying linux library..."
cp ../target/release-smaller/libspark_token_primitives.so spark-token-primitives-python/src/spark_token_primitives/libuniffi_spark_token_primitives.so

echo "Done."
