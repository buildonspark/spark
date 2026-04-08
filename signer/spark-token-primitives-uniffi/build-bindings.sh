wasm-pack build --target nodejs --out-dir ../../sdks/js/packages/spark-sdk/wasm/token-primitives/nodejs --out-name wasm_token_primitives_nodejs --no-pack
wasm-pack build --target web --out-dir ../../sdks/js/packages/spark-sdk/wasm/token-primitives/browser --out-name wasm_token_primitives_browser --no-pack

cd ../../sdks/js/packages/spark-sdk/wasm/token-primitives/nodejs
rm -f .gitignore
cd ../browser
rm -f .gitignore
cd ../../../
node ./wasm/patch-token-primitives-wasm-browser.mjs
node ./wasm/patch-token-primitives-wasm-nodejs.mjs
