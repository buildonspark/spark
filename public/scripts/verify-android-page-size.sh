#!/bin/bash

set -e

# Get the script directory and project root
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/../.." && pwd )"

echo "🔍 Verifying Android native libraries are aligned with 16kb page size..."
echo "Script directory: $SCRIPT_DIR"
echo "Project root: $PROJECT_ROOT"

# Path to the Android JNI libraries (relative to project root)
ANDROID_JNI_DIR="$PROJECT_ROOT/sdks/js/packages/spark-sdk/android/src/main/jniLibs"

# Check if the directory exists
if [ ! -d "$ANDROID_JNI_DIR" ]; then
    echo "❌ Android JNI directory not found: $ANDROID_JNI_DIR"
    echo "Please run the build script first to generate the native libraries."
    exit 1
fi

# Function to check page size alignment
check_page_size() {
    local so_file="$1"
    local arch="$2"
    
    echo "Checking $arch: $(basename "$so_file")"
    
    # Check if file exists
    if [ ! -f "$so_file" ]; then
        echo "❌ Library not found: $so_file"
        return 1
    fi
    
    # Use llvm-objdump to check ELF segment alignment
    # Look for LOAD segments with align 2**14 (16384 = 16kb)
    local alignments=$(llvm-objdump -p "$so_file" 2>/dev/null | grep "LOAD" | grep -o "align 2\*\*[0-9]*" || true)
    
    if [ -z "$alignments" ]; then
        echo "❌ Could not read ELF headers for $so_file"
        return 1
    fi
    
    # Check if all segments are aligned to 16kb (2**14)
    local invalid_alignments=""
    while IFS= read -r line; do
        if [[ "$line" =~ align\ 2\*\*([0-9]+) ]]; then
            local align_power="${BASH_REMATCH[1]}"
            if [ "$align_power" -lt 14 ]; then
                invalid_alignments+=" $line"
            fi
        fi
    done <<< "$alignments"
    
    if [ -n "$invalid_alignments" ]; then
        echo "❌ $arch: Found segments not aligned to 16kb:$invalid_alignments"
        return 1
    else
        echo "✅ $arch: All segments properly aligned to 16kb"
        return 0
    fi
}

# Verify alignment of every libuniffi_*.so present in each ABI directory.
# Discovering files dynamically (rather than hardcoding a list) keeps this
# script working for builds that ship any subset of the uniffi libraries.
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
        if ! check_page_size "$so_file" "$label ($(basename "$so_file"))"; then
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
    echo "🎉 All Android native libraries are properly aligned with 16kb page size!"
else
    echo "💥 Some Android native libraries are not properly aligned with 16kb page size!"
    exit 1
fi
