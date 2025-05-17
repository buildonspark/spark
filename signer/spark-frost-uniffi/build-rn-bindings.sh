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

echo "Generating bindings..."
# Generate Kotlin bindings for Android with native bindings
cargo run --bin uniffi-bindgen generate src/spark_frost.udl --language kotlin --out-dir "$SDK_DIR/android/src/main/java/uniffi" --config .cargo/config.toml

# Generate Swift bindings for iOS
cargo run --bin uniffi-bindgen generate src/spark_frost.udl --language swift --out-dir spark-frost-swift

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
lipo $TARGET/aarch64-apple-ios-sim/release-smaller/libspark_frost.a $TARGET/x86_64-apple-ios/release-smaller/libspark_frost.a -create -output $TARGET/lipo-ios-sim/release-smaller/libspark_frost.a
mkdir -p $TARGET/lipo-macos/release-smaller
lipo $TARGET/aarch64-apple-darwin/release-smaller/libspark_frost.a $TARGET/x86_64-apple-darwin/release-smaller/libspark_frost.a -create -output $TARGET/lipo-macos/release-smaller/libspark_frost.a

echo "Setting up directory structure..."
# Create Android JNI directories
mkdir -p "$ANDROID_JNI_DIR/arm64-v8a"
mkdir -p "$ANDROID_JNI_DIR/armeabi-v7a"
mkdir -p "$ANDROID_JNI_DIR/x86"
mkdir -p "$ANDROID_JNI_DIR/x86_64"

# Create iOS directories
mkdir -p "$IOS_DIR/spark_frostFFI.xcframework"

echo "Copying Android libraries..."
# Copy .so files to appropriate JNI directories
cp "$TARGET/aarch64-linux-android/release-smaller/libspark_frost.so" "$ANDROID_JNI_DIR/arm64-v8a/libuniffi_spark_frost.so"
cp "$TARGET/armv7-linux-androideabi/release-smaller/libspark_frost.so" "$ANDROID_JNI_DIR/armeabi-v7a/libuniffi_spark_frost.so"
cp "$TARGET/i686-linux-android/release-smaller/libspark_frost.so" "$ANDROID_JNI_DIR/x86/libuniffi_spark_frost.so"
cp "$TARGET/x86_64-linux-android/release-smaller/libspark_frost.so" "$ANDROID_JNI_DIR/x86_64/libuniffi_spark_frost.so"

echo "Copying iOS files..."

cp spark-frost-swift/spark_frostFFI.h spark-frost-swift/spark_frostFFI.xcframework/ios-arm64/spark_frostFFI.framework/Headers/spark_frostFFI.h
cp spark-frost-swift/spark_frostFFI.h spark-frost-swift/spark_frostFFI.xcframework/ios-arm64_x86_64-simulator/spark_frostFFI.framework/Headers/spark_frostFFI.h
cp spark-frost-swift/spark_frostFFI.h spark-frost-swift/spark_frostFFI.xcframework/macos-arm64_x86_64/spark_frostFFI.framework/Headers/spark_frostFFI.h
cp $TARGET/aarch64-apple-ios/release-smaller/libspark_frost.a spark-frost-swift/spark_frostFFI.xcframework/ios-arm64/spark_frostFFI.framework/spark_frostFFI
cp $TARGET/lipo-ios-sim/release-smaller/libspark_frost.a spark-frost-swift/spark_frostFFI.xcframework/ios-arm64_x86_64-simulator/spark_frostFFI.framework/spark_frostFFI
cp $TARGET/lipo-macos/release-smaller/libspark_frost.a spark-frost-swift/spark_frostFFI.xcframework/macos-arm64_x86_64/spark_frostFFI.framework/spark_frostFFI

# Copy the entire XCFramework
cp -R spark-frost-swift/spark_frostFFI.xcframework/* "$IOS_DIR/spark_frostFFI.xcframework/"

# Copy iOS libraries to the appropriate locations in the XCFramework
cp "$TARGET/aarch64-apple-ios/release-smaller/libspark_frost.a" "$IOS_DIR/spark_frostFFI.xcframework/ios-arm64/SparkFrost"
cp "$TARGET/lipo-ios-sim/release-smaller/libspark_frost.a" "$IOS_DIR/spark_frostFFI.xcframework/ios-arm64_x86_64-simulator/SparkFrost"

# Clean up temporary files
rm spark-frost-swift/spark_frostFFI.h
rm spark-frost-swift/spark_frostFFI.modulemap
rm spark-frost-swift/spark_frost.swift

echo "Verifying Android files..."
ls -l "$ANDROID_JNI_DIR/arm64-v8a/"
ls -l "$ANDROID_JNI_DIR/armeabi-v7a/"
ls -l "$ANDROID_JNI_DIR/x86/"
ls -l "$ANDROID_JNI_DIR/x86_64/"

echo "Verifying iOS files..."
ls -l "$IOS_DIR/spark_frostFFI.xcframework/"

echo "React Native bindings generated successfully!"
