#!/bin/bash
# Cursor post-edit hook for auto-formatting
# Runs the appropriate formatter based on file type and location
# Based on .lefthook.yml pre-commit configuration

set -e

# Read the file path from hook input (JSON via stdin)
INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | jq -r '.file_path // empty' 2>/dev/null)

if [ -z "$FILE_PATH" ]; then
  exit 0
fi

# Get the workspace root from Cursor's input
WORKSPACE_ROOT=$(echo "$INPUT" | jq -r '.workspace_roots[0] // empty' 2>/dev/null)
if [ -z "$WORKSPACE_ROOT" ]; then
  # Fallback: try to find project root by looking for go.mod or package.json
  WORKSPACE_ROOT=$(cd "$(dirname "$FILE_PATH")" && git rev-parse --show-toplevel 2>/dev/null || echo "")
fi

# Determine file type and run appropriate formatter
case "$FILE_PATH" in
  # Go files in spark/ directory (matches .lefthook.yml glob: "spark/**")
  */spark/*.go)
    if command -v gofmt &> /dev/null; then
      gofmt -s -w "$FILE_PATH" 2>/dev/null || true
    fi
    ;;

  # Rust files in signer/ directory (matches .lefthook.yml glob: "signer/**")
  */signer/*.rs)
    if [ -n "$WORKSPACE_ROOT" ] && [ -d "$WORKSPACE_ROOT/signer" ]; then
      cd "$WORKSPACE_ROOT/signer"
      if command -v cargo &> /dev/null; then
        cargo fmt --quiet 2>/dev/null || true
      fi
    fi
    ;;

  # TypeScript/JavaScript files in sdks/js/ (matches .lefthook.yml glob: "sdks/js/**")
  */sdks/js/*.ts|*/sdks/js/*.tsx|*/sdks/js/*.js|*/sdks/js/*.jsx)
    if [ -n "$WORKSPACE_ROOT" ] && [ -d "$WORKSPACE_ROOT/sdks/js" ]; then
      cd "$WORKSPACE_ROOT/sdks/js"
      if command -v yarn &> /dev/null; then
        yarn prettier --write "$FILE_PATH" 2>/dev/null || true
      fi
    fi
    ;;

  # Proto files - no formatter configured in lefthook, but format if clang-format available
  *.proto)
    if command -v clang-format &> /dev/null; then
      clang-format -i "$FILE_PATH" 2>/dev/null || true
    fi
    ;;

  # Fallback: Any other Go files (not in spark/)
  *.go)
    if command -v gofmt &> /dev/null; then
      gofmt -s -w "$FILE_PATH" 2>/dev/null || true
    fi
    ;;

  # Fallback: Any other Rust files (not in signer/)
  *.rs)
    # Try to find Cargo.toml in parent directories
    DIR=$(dirname "$FILE_PATH")
    while [ "$DIR" != "/" ]; do
      if [ -f "$DIR/Cargo.toml" ]; then
        cd "$DIR"
        if command -v cargo &> /dev/null; then
          cargo fmt --quiet 2>/dev/null || true
        fi
        break
      fi
      DIR=$(dirname "$DIR")
    done
    ;;

  # Fallback: Any other TypeScript/JavaScript files
  *.ts|*.tsx|*.js|*.jsx)
    if command -v yarn &> /dev/null; then
      yarn prettier --write "$FILE_PATH" 2>/dev/null || true
    fi
    ;;
esac

exit 0
