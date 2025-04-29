rustup target add aarch64-apple-ios x86_64-apple-ios
rustup target add aarch64-apple-ios-sim
rustup target add aarch64-apple-darwin x86_64-apple-darwin

TARGET=../target

cargo run --bin uniffi-bindgen generate src/spark_frost.udl --language swift --out-dir spark-frost-swift

cargo build --profile release-smaller --target x86_64-apple-darwin
cargo build --profile release-smaller --target aarch64-apple-darwin
cargo build --profile release-smaller --target x86_64-apple-ios
cargo build --profile release-smaller --target aarch64-apple-ios
cargo build --profile release-smaller --target aarch64-apple-ios-sim

mkdir -p $TARGET/lipo-ios-sim/release-smaller
lipo $TARGET/aarch64-apple-ios-sim/release-smaller/libspark_frost.a $TARGET/x86_64-apple-ios/release-smaller/libspark_frost.a -create -output $TARGET/lipo-ios-sim/release-smaller/libspark_frost.a
mkdir -p $TARGET/lipo-macos/release-smaller
lipo $TARGET/aarch64-apple-darwin/release-smaller/libspark_frost.a $TARGET/x86_64-apple-darwin/release-smaller/libspark_frost.a -create -output $TARGET/lipo-macos/release-smaller/libspark_frost.a

cp spark-frost-swift/spark_frostFFI.h spark-frost-swift/spark_frostFFI.xcframework/ios-arm64/spark_frostFFI.framework/Headers/spark_frostFFI.h
cp spark-frost-swift/spark_frostFFI.h spark-frost-swift/spark_frostFFI.xcframework/ios-arm64_x86_64-simulator/spark_frostFFI.framework/Headers/spark_frostFFI.h
cp spark-frost-swift/spark_frostFFI.h spark-frost-swift/spark_frostFFI.xcframework/macos-arm64_x86_64/spark_frostFFI.framework/Headers/spark_frostFFI.h
cp $TARGET/aarch64-apple-ios/release-smaller/libspark_frost.a spark-frost-swift/spark_frostFFI.xcframework/ios-arm64/spark_frostFFI.framework/spark_frostFFI
cp $TARGET/lipo-ios-sim/release-smaller/libspark_frost.a spark-frost-swift/spark_frostFFI.xcframework/ios-arm64_x86_64-simulator/spark_frostFFI.framework/spark_frostFFI
cp $TARGET/lipo-macos/release-smaller/libspark_frost.a spark-frost-swift/spark_frostFFI.xcframework/macos-arm64_x86_64/spark_frostFFI.framework/spark_frostFFI

rm spark-frost-swift/spark_frostFFI.h
rm spark-frost-swift/spark_frostFFI.modulemap
rm spark-frost-swift/spark_frost.swift


