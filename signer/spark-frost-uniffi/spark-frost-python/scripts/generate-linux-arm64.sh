#!/usr/bin/env bash

set -euo pipefail
python --version
pip install -r requirements.txt

echo "install cross"
cargo install cross --git https://github.com/cross-rs/cross

echo "Generating python file..."
cd ..
cargo run --bin uniffi-bindgen generate src/spark_frost.udl --language python --out-dir spark-frost-python/src/spark_frost/ --no-format

echo "Generating native binaries..."
cross build --target aarch64-unknown-linux-gnu --profile release-smaller

echo "Copying linux binary..."
cp ../target/aarch64-unknown-linux-gnu/release-smaller/libspark_frost.so spark-frost-python/src/spark_frost/libuniffi_spark_frost.so

echo "All done!"
