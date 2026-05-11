#!/bin/bash
set -e # Exit on error

rustup target add aarch64-apple-ios x86_64-apple-ios
rustup target add aarch64-apple-ios-sim
rustup target add aarch64-apple-darwin x86_64-apple-darwin

TARGET=../target

cargo run --bin uniffi-bindgen generate src/spark_token_primitives.udl --language swift --out-dir spark-token-primitives-swift

cargo build --profile release-smaller --target x86_64-apple-darwin
cargo build --profile release-smaller --target aarch64-apple-darwin
cargo build --profile release-smaller --target x86_64-apple-ios
cargo build --profile release-smaller --target aarch64-apple-ios
cargo build --profile release-smaller --target aarch64-apple-ios-sim

mkdir -p $TARGET/lipo-ios-sim/release-smaller
lipo $TARGET/aarch64-apple-ios-sim/release-smaller/libspark_token_primitives.a $TARGET/x86_64-apple-ios/release-smaller/libspark_token_primitives.a -create -output $TARGET/lipo-ios-sim/release-smaller/libspark_token_primitives.a
mkdir -p $TARGET/lipo-macos/release-smaller
lipo $TARGET/aarch64-apple-darwin/release-smaller/libspark_token_primitives.a $TARGET/x86_64-apple-darwin/release-smaller/libspark_token_primitives.a -create -output $TARGET/lipo-macos/release-smaller/libspark_token_primitives.a

cp spark-token-primitives-swift/spark_token_primitivesFFI.h spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/ios-arm64/spark_token_primitivesFFI.framework/Headers/spark_token_primitivesFFI.h
cp spark-token-primitives-swift/spark_token_primitivesFFI.h spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/ios-arm64_x86_64-simulator/spark_token_primitivesFFI.framework/Headers/spark_token_primitivesFFI.h
cp spark-token-primitives-swift/spark_token_primitivesFFI.h spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/macos-arm64_x86_64/spark_token_primitivesFFI.framework/Headers/spark_token_primitivesFFI.h
cp $TARGET/aarch64-apple-ios/release-smaller/libspark_token_primitives.a spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/ios-arm64/spark_token_primitivesFFI.framework/spark_token_primitivesFFI
cp $TARGET/lipo-ios-sim/release-smaller/libspark_token_primitives.a spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/ios-arm64_x86_64-simulator/spark_token_primitivesFFI.framework/spark_token_primitivesFFI
cp $TARGET/lipo-macos/release-smaller/libspark_token_primitives.a spark-token-primitives-swift/spark_token_primitivesFFI.xcframework/macos-arm64_x86_64/spark_token_primitivesFFI.framework/spark_token_primitivesFFI

rm spark-token-primitives-swift/spark_token_primitivesFFI.h
rm spark-token-primitives-swift/spark_token_primitivesFFI.modulemap
rm spark-token-primitives-swift/spark_token_primitives.swift
