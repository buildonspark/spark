# Generating wasm

```sh
brew install rustup llvm
# Add path to your shell, e.g. for zsh:
echo 'export PATH="/opt/homebrew/opt/llvm/bin:$PATH"' >> ~/.zshrc

cargo install wasm-pack
rustup target add wasm32-unknown-unknown
cd spark/signer/spark-frost-uniffi
cargo build
./build-bindings.sh
```