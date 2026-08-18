#!/usr/bin/env bash

set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: $0 <frost|token>..." >&2
  exit 2
fi
if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "iOS bindings must be built on macOS" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SDK_IOS_DIR="$ROOT/sdks/js/packages/spark-sdk/ios"
TARGET_DIR="$ROOT/signer/target"
targets=(
  aarch64-apple-darwin
  x86_64-apple-darwin
  aarch64-apple-ios
  aarch64-apple-ios-sim
  x86_64-apple-ios
)
rustup target add "${targets[@]}"

build_family() {
  local family="$1" crate_dir library udl generated_dir template_dir xcframework swift_file public_name
  case "$family" in
    frost)
      crate_dir="$ROOT/signer/spark-frost-uniffi"
      library="spark_frost"
      udl="spark_frost.udl"
      generated_dir="$(mktemp -d "${TMPDIR:-/tmp}/spark-frost-generated.XXXXXX")"
      template_dir="$crate_dir/spark-frost-swift/spark_frostFFI.xcframework"
      xcframework="$SDK_IOS_DIR/spark_frostFFI.xcframework"
      swift_file="spark_frost.swift"
      public_name="SparkFrost"
      ;;
    token)
      crate_dir="$ROOT/signer/spark-token-primitives-uniffi"
      library="spark_token_primitives"
      udl="spark_token_primitives.udl"
      generated_dir="$(mktemp -d "${TMPDIR:-/tmp}/spark-token-primitives-generated.XXXXXX")"
      template_dir="$crate_dir/spark-token-primitives-swift/spark_token_primitivesFFI.xcframework"
      xcframework="$SDK_IOS_DIR/spark_token_primitivesFFI.xcframework"
      swift_file="spark_token_primitives.swift"
      public_name="SparkTokenPrimitives"
      ;;
    *)
      echo "Unknown binding family: $family" >&2
      exit 2
      ;;
  esac

  rm -rf "$xcframework"
  (
    cd "$crate_dir"
    cargo run --bin uniffi-bindgen -- generate "src/$udl" \
      --language swift \
      --out-dir "$generated_dir"
    for target in "${targets[@]}"; do
      cargo build --profile release-smaller --target "$target"
    done
  )

  mkdir -p "$TARGET_DIR/lipo-ios-sim/release-smaller" "$TARGET_DIR/lipo-macos/release-smaller"
  lipo -create \
    "$TARGET_DIR/aarch64-apple-ios-sim/release-smaller/lib${library}.a" \
    "$TARGET_DIR/x86_64-apple-ios/release-smaller/lib${library}.a" \
    -output "$TARGET_DIR/lipo-ios-sim/release-smaller/lib${library}.a"
  lipo -create \
    "$TARGET_DIR/aarch64-apple-darwin/release-smaller/lib${library}.a" \
    "$TARGET_DIR/x86_64-apple-darwin/release-smaller/lib${library}.a" \
    -output "$TARGET_DIR/lipo-macos/release-smaller/lib${library}.a"

  cp -R "$template_dir" "$xcframework"
  cp "$generated_dir/$swift_file" "$SDK_IOS_DIR/$swift_file"

  local slice framework_binary source_library
  for slice in ios-arm64 ios-arm64_x86_64-simulator macos-arm64_x86_64; do
    framework_binary="$xcframework/$slice/${library}FFI.framework/${library}FFI"
    cp "$generated_dir/${library}FFI.h" \
      "$xcframework/$slice/${library}FFI.framework/Headers/${library}FFI.h"
    case "$slice" in
      ios-arm64)
        source_library="$TARGET_DIR/aarch64-apple-ios/release-smaller/lib${library}.a"
        ;;
      ios-arm64_x86_64-simulator)
        source_library="$TARGET_DIR/lipo-ios-sim/release-smaller/lib${library}.a"
        ;;
      macos-arm64_x86_64)
        source_library="$TARGET_DIR/lipo-macos/release-smaller/lib${library}.a"
        ;;
    esac
    cp "$source_library" "$framework_binary"
  done

  cp "$TARGET_DIR/aarch64-apple-ios/release-smaller/lib${library}.a" \
    "$xcframework/ios-arm64/$public_name"
  cp "$TARGET_DIR/lipo-ios-sim/release-smaller/lib${library}.a" \
    "$xcframework/ios-arm64_x86_64-simulator/$public_name"

  lipo "$xcframework/ios-arm64/${library}FFI.framework/${library}FFI" \
    -verify_arch arm64
  lipo "$xcframework/ios-arm64_x86_64-simulator/${library}FFI.framework/${library}FFI" \
    -verify_arch arm64 x86_64
  lipo "$xcframework/macos-arm64_x86_64/${library}FFI.framework/${library}FFI" \
    -verify_arch arm64 x86_64
  rm -rf "$generated_dir"
}

for family in "$@"; do
  build_family "$family"
done
