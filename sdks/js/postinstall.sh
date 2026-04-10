#!/usr/bin/env bash
set -euo pipefail

# Move to the addon directory no matter where the script is called from
cd "$(dirname "$0")/node_modules/bare-performance"

# Save original PATH and restore it regardless of exit status
ORIGINAL_PATH="$PATH"
restore_path() {
  export PATH="$ORIGINAL_PATH"
}
trap restore_path EXIT

# Use system toolchain first
export PATH="/usr/bin:$PATH"
# Tell CMake where the helper modules live (use correct list separator per platform)
if [[ "${OS:-}" == "Windows_NT" ]] || [[ -n "${MSYSTEM:-}" ]] || [[ "$(uname -s 2>/dev/null)" == MINGW* ]] || [[ "$(uname -s 2>/dev/null)" == CYGWIN* ]]; then
  LIST_SEP=";"
else
  LIST_SEP=":"
fi
export CMAKE_PREFIX_PATH="../cmake-bare${LIST_SEP}../cmake-npm${LIST_SEP}../cmake-fetch"

# Build the native addon
yarn run --top-level bare-make generate
yarn run --top-level bare-make build
yarn run --top-level bare-make install
