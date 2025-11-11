/* Convert nodejs wasm module output to ESM and inline wasm so that it can be
   used in both browser and nodejs. See https://bit.ly/4iGErRo. */

import { readFile, writeFile } from "node:fs/promises";
import fs from "node:fs";

const name = "wasm_browser";
const generatedDir = "./wasm/browser";

const content = await readFile(`${generatedDir}/${name}.js`, "utf8");

let patched = content.replace(
  "wasm_browser_bg.wasm",
  "./spark-bindings/wasm/wasm-browser-bg.wasm",
);

/* import.meta.url is widely available in ESM environments but causes a script
   parse in e.g. extension content scripts. The generated filename may vary
   (underscores/hyphens), so match any .wasm URL initializer. */
patched = patched.replace(
  /if \(typeof module_or_path === 'undefined'\)\s*\{\s*module_or_path = new URL\('[^']+\.wasm', import\.meta\.url\);\s*\}/,
  `if (typeof module_or_path === 'undefined') {
        throw new Error('WASM module path must be provided. This should be set automatically by the SDK.');
    }`,
);

await writeFile(`./src/spark-bindings/wasm/wasm-browser.js`, patched);

fs.copyFileSync(
  `${generatedDir}/${name}.d.ts`,
  `./src/spark-bindings/wasm/wasm-browser.d.ts`,
);
fs.copyFileSync(
  `${generatedDir}/${name}_bg.wasm`,
  `./src/spark-bindings/wasm/wasm-browser-bg.wasm`,
);
fs.copyFileSync(
  `${generatedDir}/${name}_bg.wasm.d.ts`,
  `./src/spark-bindings/wasm/wasm-browser-bg.wasm.d.ts`,
);
