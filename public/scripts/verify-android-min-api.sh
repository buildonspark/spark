#!/bin/bash

set -e

# Get the script directory and project root
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/../.." && pwd )"

echo "🔍 Verifying Android native libraries do not import API-30+ versioned symbols..."
echo "Script directory: $SCRIPT_DIR"
echo "Project root: $PROJECT_ROOT"

# Path to the Android JNI libraries (relative to project root)
ANDROID_JNI_DIR="$PROJECT_ROOT/sdks/js/packages/spark-sdk/android/src/main/jniLibs"

if [ ! -d "$ANDROID_JNI_DIR" ]; then
    echo "❌ Android JNI directory not found: $ANDROID_JNI_DIR"
    echo "Please run the build script first to generate the native libraries."
    exit 1
fi

# Resolve an ELF symbol reader once, up front, and fail closed if none is available
# (matching verify-android-page-size.sh) so this guard can never be silently skipped
# and report a false pass. The NDK ships llvm-objdump/llvm-readelf; fall back to
# system equivalents. We need versioned dynamic-symbol output (the @LIBC_R / (LIBC_R)
# tag on undefined imports).
READER=""
for tool in llvm-objdump objdump llvm-readelf readelf; do
    if command -v "$tool" >/dev/null 2>&1; then
        READER="$tool"
        break
    fi
done
if [ -z "$READER" ]; then
    echo "❌ No ELF symbol reader found (need one of: llvm-objdump, objdump, llvm-readelf, readelf)"
    exit 1
fi
echo "Using ELF symbol reader: $READER"

dump_dynsyms() {
    local so_file="$1"
    case "$READER" in
        llvm-objdump | objdump) "$READER" -T "$so_file" 2>/dev/null ;;
        llvm-readelf | readelf) "$READER" --dyn-syms "$so_file" 2>/dev/null ;;
    esac
}

# An undefined dynamic symbol bound to the LIBC_R version node only resolves on
# Android 11+ (API 30). bionic added the C++ unwinder (_Unwind_*) to libc.so under
# LIBC_R at API 30; on API 21-29 that version does not exist, so importing any
# LIBC_R-versioned symbol makes the library fail to dlopen. Native libs are built
# against the API-21 sysroot precisely to avoid this, so any LIBC_R import is a
# build regression (e.g. a bumped NDK API level). See the signer/*-uniffi crates'
# .cargo/config.toml for the linker setting that prevents it.
check_min_api() {
    local so_file="$1"
    local label="$2"

    echo "Checking $label: $(basename "$so_file")"

    if [ ! -f "$so_file" ]; then
        echo "❌ Library not found: $so_file"
        return 1
    fi

    local syms
    syms="$(dump_dynsyms "$so_file")" || true

    if [ -z "$syms" ]; then
        echo "❌ Could not read dynamic symbols for $so_file"
        return 1
    fi

    # Matches both objdump's "*UND* ... (LIBC_R)" and readelf's "UND name@LIBC_R".
    local bad
    bad="$(printf '%s\n' "$syms" | grep -E 'UND' | grep -F 'LIBC_R' || true)"

    if [ -n "$bad" ]; then
        echo "❌ $label imports API-30+ (LIBC_R) versioned symbols; will not load on Android 10 (API 29) or below:"
        printf '%s\n' "$bad"
        return 1
    fi

    echo "✅ $label: no API-30+ versioned imports"
    return 0
}

# Check every libuniffi_*.so present in each ABI directory. Discovering files
# dynamically (rather than hardcoding a list) keeps this script working for builds
# that ship any subset of the uniffi libraries.
failed=0
checked=0

ABIS=(
    "arm64-v8a:ARM64"
    "armeabi-v7a:ARMv7"
    "x86:x86"
    "x86_64:x86_64"
)

for abi_pair in "${ABIS[@]}"; do
    abi="${abi_pair%%:*}"
    label="${abi_pair##*:}"
    abi_dir="$ANDROID_JNI_DIR/$abi"

    if [ ! -d "$abi_dir" ]; then
        continue
    fi

    shopt -s nullglob
    for so_file in "$abi_dir"/libuniffi_*.so; do
        checked=$((checked + 1))
        if ! check_min_api "$so_file" "$label ($(basename "$so_file"))"; then
            failed=1
        fi
    done
    shopt -u nullglob
done

if [ "$checked" -eq 0 ]; then
    echo "❌ No libuniffi_*.so files found under $ANDROID_JNI_DIR. Run a build script first."
    exit 1
fi

if [ $failed -eq 0 ]; then
    echo "🎉 All Android native libraries load on the SDK's minimum Android API!"
else
    echo "💥 Some Android native libraries import API-30+ symbols and will crash on Android 10!"
    exit 1
fi
