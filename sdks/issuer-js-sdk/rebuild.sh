#!/bin/bash

# Store the current directory
CURRENT_DIR=$(pwd)

# 1. Change to sister directory spark-js-sdk
cd ../spark-js-sdk

# 2. Run yarn commands in spark-js-sdk
echo "Building spark-js-sdk..."
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
