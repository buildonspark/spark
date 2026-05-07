#!/usr/bin/env bash
set -euo pipefail

./generate-proto.sh

if ! git diff --quiet -- .; then
  echo "Spark SDK generated or formatted files are not up to date. Please run 'yarn generate:proto' in sdks/js/packages/spark-sdk and commit the changes."
  git diff -- .
  exit 1
fi

echo "Proto files are up to date"
