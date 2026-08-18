#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

bash "$ROOT/public/scripts/build-sdk-android-bindings.sh" frost
bash "$ROOT/public/scripts/build-sdk-ios-bindings.sh" frost
