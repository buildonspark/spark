#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROTO_SRC="$SCRIPT_DIR/../../protos"
PROTO_DST="$SCRIPT_DIR/protos"

mkdir -p "$PROTO_DST/validate"
trap 'rm -rf "$PROTO_DST"' EXIT

cp "$PROTO_SRC"/{common,spark,spark_token,multisig}.proto "$PROTO_DST/"
cp "$PROTO_SRC/validate/validate.proto" "$PROTO_DST/validate/"

cargo publish --manifest-path "$SCRIPT_DIR/Cargo.toml" "$@"
