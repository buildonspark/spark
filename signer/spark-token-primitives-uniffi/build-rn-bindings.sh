#!/bin/bash

set -e # Exit on error

# Get absolute paths
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
TARGET="$SCRIPT_DIR/../target"
SDK_DIR="$SCRIPT_DIR/../../sdks/js/packages/spark-sdk"
ANDROID_JNI_DIR="$SDK_DIR/android/src/main/jniLibs"
IOS_DIR="$SDK_DIR/ios"

echo "Script directory: $SCRIPT_DIR"
echo "Target directory: $TARGET"
echo "SDK directory: $SDK_DIR"

# API 21 (the SDK's minSdkVersion), not a newer level: linking against an API 30+
# sysroot makes the unwinder bind to libc.so's LIBC_R-versioned _Unwind_* symbols,
# which do not exist on Android 10 (API 29) and below. See .cargo/config.toml.
export CC_armv7_linux_androideabi=armv7a-linux-androideabi21-clang
export CC_aarch64_linux_android=aarch64-linux-android21-clang
export CC_i686_linux_android=i686-linux-android21-clang
export CC_x86_64_linux_android=x86_64-linux-android21-clang

# Add all targets
echo "Adding build targets..."
# Android targets
rustup target add aarch64-linux-android
rustup target add armv7-linux-androideabi
rustup target add i686-linux-android
rustup target add x86_64-linux-android
# iOS targets
rustup target add aarch64-apple-ios
rustup target add x86_64-apple-ios
rustup target add aarch64-apple-ios-sim
rustup target add aarch64-apple-darwin
rustup target add x86_64-apple-darwin

echo "Generating bindings..."
# Generate Kotlin bindings for Android with native bindings
cargo run --bin uniffi-bindgen generate src/spark_token_primitives.udl --language kotlin --out-dir "$SDK_DIR/android/src/main/java/uniffi" --config .cargo/config.toml

# Generate Swift bindings for iOS
cargo run --bin uniffi-bindgen generate src/spark_token_primitives.udl --language swift --out-dir spark-token-primitives-swift

echo "Building for Android..."
# Build for Android targets using release-smaller profile
cargo build --profile release-smaller --target aarch64-linux-android
cargo build --profile release-smaller --target armv7-linux-androideabi
cargo build --profile release-smaller --target i686-linux-android
cargo build --profile release-smaller --target x86_64-linux-android

echo "Building for iOS..."
# Build for iOS
cargo build --profile release-smaller --target x86_64-apple-darwin
cargo build --profile release-smaller --target aarch64-apple-darwin
cargo build --profile release-smaller --target x86_64-apple-ios
cargo build --profile release-smaller --target aarch64-apple-ios
cargo build --profile release-smaller --target aarch64-apple-ios-sim

# Create iOS universal simulator library
mkdir -p $TARGET/lipo-ios-sim/release-smaller
lipo $TARGET/aarch64-apple-ios-sim/release-smaller/libspark_token_primitives.a $TARGET/x86_64-apple-ios/release-smaller/libspark_token_primitives.a -create -output $TARGET/lipo-ios-sim/release-smaller/libspark_token_primitives.a
mkdir -p $TARGET/lipo-macos/release-smaller
lipo $TARGET/aarch64-apple-darwin/release-smaller/libspark_token_primitives.a $TARGET/x86_64-apple-darwin/release-smaller/libspark_token_primitives.a -create -output $TARGET/lipo-macos/release-smaller/libspark_token_primitives.a

echo "Setting up directory structure..."
# Create Android JNI directories
mkdir -p "$ANDROID_JNI_DIR/arm64-v8a"
mkdir -p "$ANDROID_JNI_DIR/armeabi-v7a"
mkdir -p "$ANDROID_JNI_DIR/x86"
mkdir -p "$ANDROID_JNI_DIR/x86_64"

# Create iOS directories
mkdir -p "$IOS_DIR/spark_token_primitivesFFI.xcframework"

echo "Copying Android libraries..."
# Copy .so files to appropriate JNI directories
cp "$TARGET/aarch64-linux-android/release-smaller/libspark_token_primitives.so" "$ANDROID_JNI_DIR/arm64-v8a/libuniffi_spark_token_primitives.so"
cp "$TARGET/armv7-linux-androideabi/release-smaller/libspark_token_primitives.so" "$ANDROID_JNI_DIR/armeabi-v7a/libuniffi_spark_token_primitives.so"
cp "$TARGET/i686-linux-android/release-smaller/libspark_token_primitives.so" "$ANDROID_JNI_DIR/x86/libuniffi_spark_token_primitives.so"
cp "$TARGET/x86_64-linux-android/release-smaller/libspark_token_primitives.so" "$ANDROID_JNI_DIR/x86_64/libuniffi_spark_token_primitives.so"

echo "Copying iOS files..."

cp spark-token-primitives-swift/spark_token_primitives.swift "$IOS_DIR/spark_token_primitives.swift"
cp spark-token-primitives-swift/spark_token_primitivesFFI.h spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/ios-arm64/spark_token_primitivesFFI.framework/Headers/spark_token_primitivesFFI.h
cp spark-token-primitives-swift/spark_token_primitivesFFI.h spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/ios-arm64_x86_64-simulator/spark_token_primitivesFFI.framework/Headers/spark_token_primitivesFFI.h
cp spark-token-primitives-swift/spark_token_primitivesFFI.h spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/macos-arm64_x86_64/spark_token_primitivesFFI.framework/Headers/spark_token_primitivesFFI.h
cp $TARGET/aarch64-apple-ios/release-smaller/libspark_token_primitives.a spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/ios-arm64/spark_token_primitivesFFI.framework/spark_token_primitivesFFI
cp $TARGET/lipo-ios-sim/release-smaller/libspark_token_primitives.a spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/ios-arm64_x86_64-simulator/spark_token_primitivesFFI.framework/spark_token_primitivesFFI
cp $TARGET/lipo-macos/release-smaller/libspark_token_primitives.a spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/macos-arm64_x86_64/spark_token_primitivesFFI.framework/spark_token_primitivesFFI

# Copy the entire XCFramework
cp -R spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/* "$IOS_DIR/spark_token_primitivesFFI.xcframework/"

# Copy iOS libraries to the appropriate locations in the XCFramework
cp "$TARGET/aarch64-apple-ios/release-smaller/libspark_token_primitives.a" "$IOS_DIR/spark_token_primitivesFFI.xcframework/ios-arm64/SparkTokenPrimitives"
cp "$TARGET/lipo-ios-sim/release-smaller/libspark_token_primitives.a" "$IOS_DIR/spark_token_primitivesFFI.xcframework/ios-arm64_x86_64-simulator/SparkTokenPrimitives"

# Clean up temporary files
rm spark-token-primitives-swift/spark_token_primitivesFFI.h
rm spark-token-primitives-swift/spark_token_primitivesFFI.modulemap
rm spark-token-primitives-swift/spark_token_primitives.swift

echo "Verifying Android files..."
ls -l "$ANDROID_JNI_DIR/arm64-v8a/"
ls -l "$ANDROID_JNI_DIR/armeabi-v7a/"
ls -l "$ANDROID_JNI_DIR/x86/"
ls -l "$ANDROID_JNI_DIR/x86_64/"

echo "Verifying iOS files..."
ls -l "$IOS_DIR/spark_token_primitivesFFI.xcframework/"

echo "React Native bindings generated successfully!"

echo "Verifying 16kb page size alignment..."
bash "$SCRIPT_DIR/../../public/scripts/verify-android-page-size.sh"

echo "Verifying Android minimum API compatibility (no LIBC_R imports)..."
bash "$SCRIPT_DIR/../../public/scripts/verify-android-min-api.sh"
