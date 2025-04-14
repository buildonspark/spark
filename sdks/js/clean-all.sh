#! /bin/bash

yarn clean
find . -type d -name "node_modules" -exec rm -rf {} +
find . -type d -name ".turbo" -exec rm -rf {} +
find . -type f -name "install-state.gz" -exec rm -rf {} +