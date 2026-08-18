#!/usr/bin/env bash

set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: $0 <frost|token>..." >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SDK_DIR="$ROOT/sdks/js/packages/spark-sdk"
TARGET_DIR="$ROOT/signer/target"
JNI_DIR="$SDK_DIR/android/src/main/jniLibs"
NDK_ROOT="${ANDROID_NDK_ROOT:-${ANDROID_NDK_HOME:-}}"

if [[ -z "$NDK_ROOT" ]]; then
  echo "ANDROID_NDK_ROOT or ANDROID_NDK_HOME must point to Android NDK r25c" >&2
  exit 1
fi

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) host_tags=(linux-x86_64) ;;
  Darwin-arm64) host_tags=(darwin-arm64 darwin-x86_64) ;;
  Darwin-x86_64) host_tags=(darwin-x86_64) ;;
  *)
    echo "Unsupported NDK host: $(uname -s)-$(uname -m)" >&2
    exit 1
    ;;
esac

ndk_bin=""
for host_tag in "${host_tags[@]}"; do
  candidate="$NDK_ROOT/toolchains/llvm/prebuilt/$host_tag/bin"
  if [[ -d "$candidate" ]]; then
    ndk_bin="$candidate"
    break
  fi
done
if [[ -z "$ndk_bin" ]]; then
  echo "No compatible NDK toolchain found under $NDK_ROOT/toolchains/llvm/prebuilt" >&2
  exit 1
fi
export PATH="$ndk_bin:$PATH"
export CC_armv7_linux_androideabi=armv7a-linux-androideabi21-clang
export CC_aarch64_linux_android=aarch64-linux-android21-clang
export CC_i686_linux_android=i686-linux-android21-clang
export CC_x86_64_linux_android=x86_64-linux-android21-clang

targets=(
  aarch64-linux-android
  armv7-linux-androideabi
  i686-linux-android
  x86_64-linux-android
)
rustup target add "${targets[@]}"

build_family() {
  local family="$1" crate_dir library udl kotlin_output
  case "$family" in
    frost)
      crate_dir="$ROOT/signer/spark-frost-uniffi"
      library="spark_frost"
      udl="spark_frost.udl"
      kotlin_output="$SDK_DIR/android/src/main/java/uniffi/uniffi/spark_frost"
      ;;
    token)
      crate_dir="$ROOT/signer/spark-token-primitives-uniffi"
      library="spark_token_primitives"
      udl="spark_token_primitives.udl"
      kotlin_output="$SDK_DIR/android/src/main/java/uniffi/uniffi/spark_token_primitives"
      ;;
    *)
      echo "Unknown binding family: $family" >&2
      exit 2
      ;;
  esac

  rm -rf "$kotlin_output"
  (
    cd "$crate_dir"
    cargo run --bin uniffi-bindgen -- generate "src/$udl" \
      --language kotlin \
      --out-dir "$SDK_DIR/android/src/main/java/uniffi" \
      --config .cargo/config.toml
    for target in "${targets[@]}"; do
      cargo build --profile release-smaller --target "$target"
    done
  )

  mkdir -p "$JNI_DIR/arm64-v8a" "$JNI_DIR/armeabi-v7a" "$JNI_DIR/x86" "$JNI_DIR/x86_64"
  cp "$TARGET_DIR/aarch64-linux-android/release-smaller/lib${library}.so" \
    "$JNI_DIR/arm64-v8a/libuniffi_${library}.so"
  cp "$TARGET_DIR/armv7-linux-androideabi/release-smaller/lib${library}.so" \
    "$JNI_DIR/armeabi-v7a/libuniffi_${library}.so"
  cp "$TARGET_DIR/i686-linux-android/release-smaller/lib${library}.so" \
    "$JNI_DIR/x86/libuniffi_${library}.so"
  cp "$TARGET_DIR/x86_64-linux-android/release-smaller/lib${library}.so" \
    "$JNI_DIR/x86_64/libuniffi_${library}.so"
  chmod 755 "$JNI_DIR"/*/"libuniffi_${library}.so"
}

for family in "$@"; do
  build_family "$family"
done

bash "$ROOT/public/scripts/verify-android-page-size.sh"
bash "$ROOT/public/scripts/verify-android-min-api.sh"
