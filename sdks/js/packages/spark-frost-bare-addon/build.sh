#!/usr/bin/env bash
set -euo pipefail

# Save original PATH and restore it regardless of exit status
ORIGINAL_PATH="$PATH"
restore_path() {
  export PATH="$ORIGINAL_PATH"
}
trap restore_path EXIT

# Use system toolchain first
export PATH="/usr/bin:$PATH"

# Build the native addon
yarn run bare-make generate
yarn run bare-make build
yarn run bare-make install
case "$(uname -s)" in
  MINGW*|CYGWIN*|MSYS*)
    node scripts/normalize-win32-prebuilds.js
    ;;
esac
