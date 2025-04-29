#!/bin/bash

# Store the current directory
CURRENT_DIR=$(pwd)

# 1. Change to sister directory spark-sdk
cd ../spark-sdk

# 2. Run yarn commands in spark-sdk
echo "Building spark-sdk..."
yarn clean
yarn install
yarn generate:proto
yarn build

# 3. Return to original directory
cd "$CURRENT_DIR"

# 4. Run yarn commands in current directory
echo "Building current project..."
yarn clean:all
rm -f yarn.lock
yarn install
yarn build
yarn generate:proto

echo "Build process completed!"
