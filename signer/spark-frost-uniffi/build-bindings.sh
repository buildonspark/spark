wasm-pack build --target nodejs --out-dir ../../sdks/js/packages/spark-sdk/wasm/nodejs --out-name wasm_nodejs --no-pack
wasm-pack build --target web --out-dir ../../sdks/js/packages/spark-sdk/wasm/browser --out-name wasm_browser --no-pack

cd ../../sdks/js/packages/spark-sdk/wasm/nodejs
rm .gitignore
cd ../../sdks/js/packages/spark-sdk/wasm/browser
rm .gitignore
yarn
yarn patch-wasm