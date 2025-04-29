#!/usr/bin/env bash

set -euo pipefail
python3 --version
pip install --user -r requirements.txt

echo "Generating python file..."
cd ..
cargo run --bin uniffi-bindgen generate src/spark_frost.udl --language python --out-dir spark-frost-python/src/spark_frost/ --no-format

echo "Generating native binaries..."
rustup target add x86_64-apple-darwin
cargo build --profile release-smaller --target x86_64-apple-darwin

echo "Copying libraries dylib..."
cp ../target/x86_64-apple-darwin/release-smaller/libspark_frost.dylib spark-frost-python/src/spark_frost/libuniffi_spark_frost.dylib

echo "All done!"

