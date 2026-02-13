#!/bin/bash
set -e

FILE_PATH="$1"

# Go files: format with gofmt (with simplification)
if [[ "$FILE_PATH" == */spark/* ]] && [[ "$FILE_PATH" == *.go ]]; then
  echo "Formatting Go file: $FILE_PATH"
  gofmt -s -w "$FILE_PATH"
fi

# TypeScript/JavaScript files: format with prettier
if [[ "$FILE_PATH" == */sdks/js/* ]] && [[ "$FILE_PATH" == *.ts || "$FILE_PATH" == *.tsx || "$FILE_PATH" == *.js ]]; then
  cd "$CLAUDE_PROJECT_DIR/sdks/js"
  echo "Formatting TypeScript/JavaScript file: $FILE_PATH"
  yarn prettier --write "$FILE_PATH"
fi
