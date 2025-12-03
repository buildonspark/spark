# Generating wasm

```sh
# Install Rust and LLVM
brew install rustup llvm
# Add path to your shell, e.g. for zsh:
echo 'export PATH="/opt/homebrew/opt/llvm/bin:$PATH"' >> ~/.zshrc

# Install Rust tools
cargo install wasm-pack
rustup target add wasm32-unknown-unknown
```

## Install Android NDK

### Option 1 (recommended for easier debugging): Using Android Studio
1. Open Android Studio
2. Go to Tools → SDK Manager
3. Click on the "SDK Tools" tab
4. Check the box next to "NDK (Side by side)"
5. Check the box next to "Android SDK command-line tools"
6. Click "Apply" and wait for the download to complete
7. Add Android Studio NDK path with the version you downloaded above:
```sh
echo 'export ANDROID_NDK_HOME=$HOME/Library/Android/sdk/ndk/27.2.12479018' >> ~/.zshrc
# Add Android SDK command-line tools to path:
echo 'export PATH=$HOME/Library/Android/sdk/cmdline-tools/latest/bin:$PATH' >> ~/.zshrc
```

### Option 2: Using homebrew
```sh
brew install android-commandlinetools
sdkmanager --install "ndk;27.2.12479018" # or whatever version you prefer
echo 'export ANDROID_NDK_HOME=/opt/homebrew/share/android-commandlinetools/ndk/27.2.12479018' >> ~/.zshrc
```

## Finish configuation

```sh
# Add Android NDK environment variables to your shell
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
./build-rn-bindings.sh
```
