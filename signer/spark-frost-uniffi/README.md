# Generating wasm

```sh
# Install Rust and LLVM
brew install rustup llvm
# Add path to your shell, e.g. for zsh:
echo 'export PATH="/opt/homebrew/opt/llvm/bin:$PATH"' >> ~/.zshrc

# Install Rust tools
cargo install wasm-pack
rustup target add wasm32-unknown-unknown

# Install Android NDK
# Option 1: Using Android Studio
# 1. Open Android Studio
# 2. Go to Tools → SDK Manager
# 3. Click on the "SDK Tools" tab
# 4. Check the box next to "NDK (Side by side)"
# 5. Click "Apply" and wait for the download to complete

# Option 2: Using command line
brew install android-commandlinetools
sdkmanager --install "ndk;25.2.9519653"  # or whatever version you prefer

# Add Android NDK environment variables to your shell
echo 'export ANDROID_NDK_HOME=$HOME/Library/Android/sdk/ndk/25.2.9519653' >> ~/.zshrc
echo 'export PATH=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/darwin-x86_64/bin:$PATH' >> ~/.zshrc

# Reload shell configuration
source ~/.zshrc

# Add Android targets to Rust
rustup target add aarch64-linux-android
rustup target add armv7-linux-androideabi
rustup target add i686-linux-android
rustup target add x86_64-linux-android

# Create .cargo/config.toml for Android build configuration
mkdir -p .cargo
cat > .cargo/config.toml << 'EOL'
[target.aarch64-linux-android]
linker = "aarch64-linux-android33-clang"
rustflags = ["-C", "link-arg=-Wl,--allow-multiple-definition"]

[target.armv7-linux-androideabi]
linker = "armv7a-linux-androideabi33-clang"
rustflags = ["-C", "link-arg=-Wl,--allow-multiple-definition"]

[target.i686-linux-android]
linker = "i686-linux-android33-clang"
rustflags = ["-C", "link-arg=-Wl,--allow-multiple-definition"]

[target.x86_64-linux-android]
linker = "x86_64-linux-android33-clang"
rustflags = ["-C", "link-arg=-Wl,--allow-multiple-definition"]
EOL

# Build and generate bindings
cd spark/signer/spark-frost-uniffi
cargo build
./build-bindings.sh
```

Note: Make sure to adjust the NDK version number (25.2.9519653) to match the version you installed.
