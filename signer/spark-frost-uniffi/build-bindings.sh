wasm-pack build --target nodejs --out-dir ../../sdks/js/packages/spark-sdk/wasm/nodejs --out-name spark_bindings_nodejs --no-pack
cd ../../sdks/js/packages/spark-sdk/wasm/nodejs
rm .gitignore
yarn
yarn patch-wasm