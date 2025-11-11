import fs from "node:fs";
import path from "node:path";

const src = path.resolve("src/spark-bindings/wasm/wasm-browser-bg.wasm");
const dstDir = path.resolve("dist/spark-bindings/wasm");
fs.mkdirSync(dstDir, { recursive: true });
fs.copyFileSync(src, path.join(dstDir, "wasm-browser-bg.wasm"));
console.log(
  "Copied wasm-browser-bg.wasm -> dist/spark-bindings/wasm/wasm-browser-bg.wasm",
);
