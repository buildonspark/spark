#!/usr/bin/env bash

set -euo pipefail
python --version
pip install -r requirements.txt

echo "Generating python file..."
cd ..
cargo run --bin uniffi-bindgen generate src/spark_frost.udl --language python --out-dir spark-frost-python/src/spark_frost/ --no-format

echo "Generating native binaries..."
cargo build --profile release-smaller

echo "Copying linux binary..."
cp ../target/release-smaller/libspark_frost.so spark-frost-python/src/spark_frost/libuniffi_spark_frost.so

echo "All done!"
