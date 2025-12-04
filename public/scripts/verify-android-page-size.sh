#!/bin/bash

set -e

# Get the script directory and project root
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"

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

# Check all Android architectures
failed=0

# ARM64
if ! check_page_size "$ANDROID_JNI_DIR/arm64-v8a/libuniffi_spark_frost.so" "ARM64"; then
    failed=1
fi

# ARMv7
if ! check_page_size "$ANDROID_JNI_DIR/armeabi-v7a/libuniffi_spark_frost.so" "ARMv7"; then
    failed=1
fi

# x86
if ! check_page_size "$ANDROID_JNI_DIR/x86/libuniffi_spark_frost.so" "x86"; then
    failed=1
fi

# x86_64
if ! check_page_size "$ANDROID_JNI_DIR/x86_64/libuniffi_spark_frost.so" "x86_64"; then
    failed=1
fi

if [ $failed -eq 0 ]; then
    echo "🎉 All Android native libraries are properly aligned with 16kb page size!"
else
    echo "💥 Some Android native libraries are not properly aligned with 16kb page size!"
    exit 1
fi
